package validator

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
)

// PackageLimits bound the work per QTI package.
type PackageLimits struct {
	MaxFiles            int   // entries in the ZIP, directories included
	MaxFileSize         int64 // uncompressed bytes per XML entry
	MaxUncompressedSize int64 // uncompressed bytes over all XML entries
}

// DefaultPackageLimits are used for zero fields.
var DefaultPackageLimits = PackageLimits{
	MaxFiles:            1000,
	MaxFileSize:         10 << 20,
	MaxUncompressedSize: 256 << 20,
}

// PackageValidationResult is the result of validating a QTI package.
type PackageValidationResult struct {
	Valid   bool              `json:"valid"`
	Version string            `json:"version,omitempty"` // QTI version the package was validated against
	Errors  []ValidationError `json:"errors,omitempty"`
	Files   []FileResult      `json:"files"`
	Outcome Outcome           `json:"-"`
}

// FileResult is the result for one XML file in a package. Skipped files are
// XML that is not a QTI document (for example a stray .xml resource); they do
// not make the package invalid.
type FileResult struct {
	File     string            `json:"file"`
	Valid    bool              `json:"valid"`
	Skipped  bool              `json:"skipped,omitempty"`
	Schema   string            `json:"schema,omitempty"`
	Version  string            `json:"version,omitempty"`
	Errors   []ValidationError `json:"errors,omitempty"`
	Warnings []ValidationError `json:"warnings,omitempty"`
}

const manifestName = "imsmanifest.xml"

// ValidatePackage validates every XML file in a QTI package (a ZIP) against
// the schemas. Entries are decompressed one at a time and streamed into the
// validator; nothing is written to disk. Media files are not read.
//
// Every document is validated against one QTI version: opts.Version when
// set, otherwise the version the manifest declares, otherwise the latest.
//
// This checks the documents, not the package semantics: it does not yet
// verify that the manifest and its resources reference each other correctly.
func (v *Validator) ValidatePackage(ctx context.Context, r io.ReaderAt, size int64, limits PackageLimits, opts ValidateOptions) PackageValidationResult {
	limits = withPackageDefaults(limits)
	res := PackageValidationResult{Files: []FileResult{}, Outcome: OutcomeValid}
	fail := func(outcome Outcome, e ValidationError) PackageValidationResult {
		res.Errors = append(res.Errors, e)
		res.Outcome = worse(res.Outcome, outcome)
		return res
	}

	zr, err := zip.NewReader(r, size)
	if err != nil {
		return fail(OutcomeMalformed, ValidationError{Code: CodeInvalidZIP, Message: err.Error()})
	}
	if len(zr.File) > limits.MaxFiles {
		return fail(OutcomeTooLarge, ValidationError{Code: CodeTooManyFiles,
			Message: fmt.Sprintf("package has %d entries, the limit is %d", len(zr.File), limits.MaxFiles)})
	}

	manifest := v.readManifest(zr, limits.MaxFileSize)
	opts.DefaultVersion = manifest.version
	res.Version = opts.Version
	if res.Version == "" {
		res.Version = opts.DefaultVersion
	}

	hasManifest := false
	budget := limits.MaxUncompressedSize
	for _, f := range zr.File {
		if !safeEntryName(f.Name) {
			fail(OutcomeInvalid, ValidationError{Code: CodeUnsafePath, File: f.Name,
				Message: "entry name is absolute or leaves the package root"})
			continue
		}
		if f.FileInfo().IsDir() || !strings.EqualFold(path.Ext(f.Name), ".xml") {
			continue
		}
		if f.Name == manifestName {
			hasManifest = true
		}
		if err := ctx.Err(); err != nil {
			return fail(OutcomeInternal, ValidationError{Code: CodeInternal, Message: "validation interrupted: " + err.Error()})
		}
		fr := v.validateEntry(ctx, f, limits.MaxFileSize, &budget, opts, manifest.qtiFiles[f.Name])
		if fr.Outcome == OutcomeTooLarge {
			return fail(OutcomeTooLarge, ValidationError{Code: CodeTooLarge,
				Message: fmt.Sprintf("XML in the package exceeds %d uncompressed bytes", limits.MaxUncompressedSize)})
		}
		res.Files = append(res.Files, fr.FileResult)
		res.Outcome = worse(res.Outcome, fr.Outcome)
	}
	if !hasManifest {
		fail(OutcomeInvalid, ValidationError{Code: CodeMissingManifest, Message: "package has no " + manifestName + " at its root"})
	}
	res.Valid = res.Outcome == OutcomeValid
	return res
}

// manifestInfo is what the package needs from its manifest before the
// documents are validated.
type manifestInfo struct {
	// version is the version the manifest declares when it is a supported
	// one, and otherwise the latest. The manifest's own validation reports
	// an unsupported version.
	version string
	// qtiFiles are the files of resources whose type is a QTI type
	// (imsqti_item_xmlv3p0 and the like). Such a file must be a QTI
	// document; any other XML file that is not one is skipped.
	qtiFiles map[string]bool
}

func (v *Validator) readManifest(zr *zip.Reader, maxSize int64) manifestInfo {
	info := manifestInfo{version: LatestVersion(), qtiFiles: map[string]bool{}}
	for _, f := range zr.File {
		if f.Name != manifestName || f.UncompressedSize64 > uint64(maxSize) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			break
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxSize))
		rc.Close()
		if err != nil {
			break
		}
		if declared := manifestSchemaVersion(data).value; IsSupportedVersion(declared) {
			info.version = declared
		}
		info.qtiFiles = manifestQTIFiles(data)
		break
	}
	return info
}

// manifestQTIFiles lists the href and file hrefs of the manifest's
// resources whose type starts with "imsqti_", as paths inside the package.
// QTI 3 content packaging gives each QTI resource such a type, so these
// files must be QTI documents.
func manifestQTIFiles(data []byte) map[string]bool {
	files := map[string]bool{}
	dec := xml.NewDecoder(bytes.NewReader(data))
	inQTI := false
	add := func(href string) {
		if href == "" {
			return
		}
		if u, err := url.PathUnescape(href); err == nil {
			href = u
		}
		files[path.Clean(href)] = true
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return files
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "resource":
				inQTI = strings.HasPrefix(attrValue(t, "type"), "imsqti_")
				if inQTI {
					add(attrValue(t, "href"))
				}
			case "file":
				if inQTI {
					add(attrValue(t, "href"))
				}
			}
		case xml.EndElement:
			if t.Name.Local == "resource" {
				inQTI = false
			}
		}
	}
}

type entryResult struct {
	FileResult
	Outcome Outcome
}

// validateEntry validates one XML entry. Its Outcome is OutcomeTooLarge only
// when the package-wide budget ran out, which aborts the package.
// A file the manifest lists as a QTI resource is never skipped: if it is not
// a QTI document, that is an error.
func (v *Validator) validateEntry(ctx context.Context, f *zip.File, maxSize int64, budget *int64, opts ValidateOptions, qtiResource bool) entryResult {
	out := entryResult{FileResult: FileResult{File: f.Name}, Outcome: OutcomeInvalid}
	limit := maxSize
	if f.UncompressedSize64 > uint64(limit) {
		out.Errors = []ValidationError{{Code: CodeTooLarge, File: f.Name,
			Message: fmt.Sprintf("file is larger than %d bytes", limit)}}
		return out
	}
	rc, err := f.Open()
	if err != nil {
		out.Errors = []ValidationError{{Code: CodeInvalidZIP, File: f.Name, Message: err.Error()}}
		return out
	}
	defer rc.Close()

	// The header size above is only a hint; the limit on the bytes actually
	// inflated is what stops a ZIP bomb.
	readLimit := min(limit, *budget)
	counted := &countingReader{r: rc}
	res := v.validateLimited(ctx, counted, readLimit, opts)
	*budget -= counted.n

	out.Schema, out.Version = res.Schema, res.Version
	out.Errors, out.Warnings = res.Errors, res.Warnings
	for i := range out.Errors {
		out.Errors[i].File = f.Name
	}
	for i := range out.Warnings {
		out.Warnings[i].File = f.Name
	}
	switch {
	case counted.err != nil:
		// archive/zip reports a corrupt entry, a CRC mismatch, or more data
		// than the header declares (a ZIP bomb with a lying header).
		out.Schema = ""
		out.Errors = []ValidationError{{Code: CodeInvalidZIP, File: f.Name, Message: counted.err.Error()}}
	case res.Outcome == OutcomeValid:
		out.Valid, out.Outcome = true, OutcomeValid
	case res.Outcome == OutcomeTooLarge && readLimit < limit:
		out.Outcome = OutcomeTooLarge
	case len(res.Errors) == 1 && res.Errors[0].Code == CodeUnsupportedDocument && !qtiResource:
		out.Valid, out.Skipped, out.Outcome = true, true, OutcomeValid
	case res.Outcome == OutcomeInternal:
		out.Outcome = OutcomeInternal
	}
	// Otherwise OutcomeInvalid: inside a package a malformed or oversized
	// document makes the package invalid, the request itself was fine.
	return out
}

func (v *Validator) validateLimited(ctx context.Context, r io.Reader, limit int64, opts ValidateOptions) ValidationResult {
	sub := *v
	sub.limits.MaxDocumentSize = limit
	return sub.Validate(ctx, r, opts)
}

// safeEntryName rejects names that would escape a directory if the package
// were extracted: absolute paths, drive letters, backslashes and "..".
func safeEntryName(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") ||
		len(name) >= 2 && name[1] == ':' {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

func withPackageDefaults(l PackageLimits) PackageLimits {
	if l.MaxFiles <= 0 {
		l.MaxFiles = DefaultPackageLimits.MaxFiles
	}
	if l.MaxFileSize <= 0 {
		l.MaxFileSize = DefaultPackageLimits.MaxFileSize
	}
	if l.MaxUncompressedSize <= 0 {
		l.MaxUncompressedSize = DefaultPackageLimits.MaxUncompressedSize
	}
	return l
}

// countingReader counts the bytes read from a ZIP entry and keeps the
// entry's own read error apart from validation errors.
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
