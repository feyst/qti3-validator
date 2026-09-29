package app

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/kennisnet/qti3-validator/internal/domain/qti"
)

// ValidatePackage validates every XML file in a QTI content package. Entries
// are inflated one at a time and streamed into the document validation;
// nothing is written to disk and media files are not read.
//
// Every document is validated against one QTI version: the one the query
// forces, otherwise the one the manifest declares, otherwise the latest.
//
// This checks the documents, not the package semantics: it does not verify
// that the manifest and its resources reference each other correctly.
func (v *Validator) ValidatePackage(ctx context.Context, q ValidatePackage) qti.PackageResult {
	limits := q.Limits.WithDefaults()
	res := qti.PackageResult{Files: []qti.FileResult{}, Outcome: qti.OutcomeValid}

	archive, err := v.openArchive(q.Package, q.Size)
	if err != nil {
		res.Fail(qti.OutcomeMalformed, qti.Finding{Code: qti.CodeInvalidZIP, Message: err.Error()})
		return res
	}
	entries := archive.Entries()
	if len(entries) > limits.MaxFiles {
		res.Fail(qti.OutcomeTooLarge, qti.Finding{
			Code:    qti.CodeTooManyFiles,
			Message: fmt.Sprintf("package has %d entries, the limit is %d", len(entries), limits.MaxFiles),
		})
		return res
	}

	manifest := readManifest(entries, limits.MaxFileSize)
	req := qti.VersionRequest{Forced: q.Version, Default: manifest.version}
	res.Version = q.Version
	if res.Version == "" {
		res.Version = manifest.version
	}

	hasManifest := false
	budget := limits.MaxUncompressedSize
	for _, e := range entries {
		name := e.Name()
		if !qti.SafeEntryName(name) {
			res.Fail(qti.OutcomeInvalid, qti.Finding{
				Code: qti.CodeUnsafePath, File: name,
				Message: "entry name is absolute or leaves the package root",
			})
			continue
		}
		if e.IsDir() || !strings.EqualFold(path.Ext(name), ".xml") {
			continue
		}
		if name == qti.ManifestName {
			hasManifest = true
		}
		if err := ctx.Err(); err != nil {
			res.Fail(qti.OutcomeInternal, qti.Finding{Code: qti.CodeInternal, Message: "validation interrupted: " + err.Error()})
			return res
		}
		fr, outcome := v.validateEntry(ctx, e, limits.MaxFileSize, &budget, req, manifest.qtiFiles[name])
		if outcome == qti.OutcomeTooLarge {
			res.Fail(qti.OutcomeTooLarge, qti.Finding{
				Code:    qti.CodeTooLarge,
				Message: fmt.Sprintf("XML in the package exceeds %d uncompressed bytes", limits.MaxUncompressedSize),
			})
			return res
		}
		res.Files = append(res.Files, fr)
		res.Outcome = res.Outcome.Worse(outcome)
	}
	if !hasManifest {
		res.Fail(qti.OutcomeInvalid, qti.Finding{Code: qti.CodeMissingManifest, Message: "package has no " + qti.ManifestName + " at its root"})
	}
	res.Valid = res.Outcome == qti.OutcomeValid
	return res
}

// manifestInfo is what the package needs from its manifest before the
// documents are validated.
type manifestInfo struct {
	// version is the version the manifest declares when it is a supported
	// one, and otherwise the latest. The manifest's own validation reports
	// an unsupported version.
	version string
	// qtiFiles are the files of the manifest's QTI resources. Such a file
	// must be a QTI document; any other XML file that is not one is skipped.
	qtiFiles map[string]bool
}

func readManifest(entries []ArchiveEntry, maxSize int64) manifestInfo {
	info := manifestInfo{version: qti.LatestVersion().Name, qtiFiles: map[string]bool{}}
	for _, e := range entries {
		if e.Name() != qti.ManifestName || exceeds(e.DeclaredSize(), maxSize) {
			continue
		}
		data, err := readEntry(e, maxSize)
		if err != nil {
			break
		}
		if declared := manifestSchemaVersion(data).Value; qti.IsSupportedVersion(declared) {
			info.version = declared
		}
		info.qtiFiles = manifestQTIFiles(data)
		break
	}
	return info
}

func readEntry(e ArchiveEntry, maxSize int64) ([]byte, error) {
	rc, err := e.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, maxSize))
}

// validateEntry validates one XML entry. Its outcome is OutcomeTooLarge only
// when the package-wide budget ran out, which aborts the package. A file the
// manifest lists as a QTI resource is never skipped: if it is not a QTI
// document, that is an error.
func (v *Validator) validateEntry(ctx context.Context, e ArchiveEntry, maxSize int64, budget *int64,
	req qti.VersionRequest, qtiResource bool,
) (qti.FileResult, qti.Outcome) {
	name := e.Name()
	out := qti.FileResult{File: name}
	if exceeds(e.DeclaredSize(), maxSize) {
		out.Errors = []qti.Finding{{Code: qti.CodeTooLarge, File: name, Message: fmt.Sprintf("file is larger than %d bytes", maxSize)}}
		return out, qti.OutcomeInvalid
	}
	rc, err := e.Open()
	if err != nil {
		out.Errors = []qti.Finding{{Code: qti.CodeInvalidZIP, File: name, Message: err.Error()}}
		return out, qti.OutcomeInvalid
	}
	defer rc.Close()

	// The declared size above is only a hint; the limit on the bytes actually
	// inflated is what stops a ZIP bomb.
	readLimit := min(maxSize, *budget)
	counted := &countingReader{r: rc}
	limits := v.limits
	limits.MaxDocumentSize = readLimit
	res := v.validateReader(ctx, counted, req, limits)
	*budget -= counted.n

	out.Schema, out.Version = res.Schema, res.Version
	out.Errors, out.Warnings = inFile(res.Errors, name), inFile(res.Warnings, name)
	switch {
	case counted.err != nil:
		// A corrupt entry, a CRC mismatch, or more data than the header
		// declares: a ZIP bomb with a lying header.
		out.Schema = ""
		out.Errors = []qti.Finding{{Code: qti.CodeInvalidZIP, File: name, Message: counted.err.Error()}}
	case res.Outcome == qti.OutcomeValid:
		out.Valid = true
		return out, qti.OutcomeValid
	case res.Outcome == qti.OutcomeTooLarge && readLimit < maxSize:
		return out, qti.OutcomeTooLarge
	case len(res.Errors) == 1 && res.Errors[0].Code == qti.CodeUnsupportedDocument && !qtiResource:
		out.Valid, out.Skipped = true, true
		return out, qti.OutcomeValid
	case res.Outcome == qti.OutcomeInternal:
		return out, qti.OutcomeInternal
	}
	// Inside a package a malformed or oversized document makes the package
	// invalid; the request itself was fine.
	return out, qti.OutcomeInvalid
}

// exceeds reports whether a declared size is over a limit.
func exceeds(declared uint64, limit int64) bool {
	if limit < 0 {
		return true
	}
	return declared > uint64(limit)
}

// inFile sets the package entry of findings.
func inFile(findings []qti.Finding, name string) []qti.Finding {
	for i := range findings {
		findings[i].File = name
	}
	return findings
}

// countingReader counts the bytes read from an entry and keeps the entry's
// own read error apart from validation errors.
type countingReader struct {
	r   io.Reader
	n   int64
	err error
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if err != nil && err != io.EOF {
		c.err = err
	}
	return n, err
}
