package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"qti3-validator/internal/domain/qti"
)

// ValidatePackage validates every XML file in a QTI content package. Entries
// are inflated one at a time and streamed into the document validation;
// nothing is written to disk and media files are not read.
//
// Every document is validated against one QTI version: the one the query
// forces, otherwise the one the manifest declares, otherwise the latest.
//
// After the documents, it checks that the references between the files of
// the package resolve (qti.CheckReferences).
func (v *Validator) ValidatePackage(ctx context.Context, q ValidatePackage) qti.PackageResult {
	limits := q.Limits.WithDefaults()
	res := qti.PackageResult{Files: []qti.FileResult{}, Outcome: qti.OutcomeValid}

	archive, err := v.openArchive(q.Package, q.Size, limits.MaxFiles)
	if errors.Is(err, ErrTooManyEntries) {
		res.Fail(qti.OutcomeTooLarge, qti.Finding{
			Code:    qti.CodeTooManyFiles,
			Message: fmt.Sprintf("package has more entries than the limit of %d", limits.MaxFiles),
		})
		return res
	}
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
	contents := qti.PackageContents{Documents: map[string]qti.PackageDocument{}, Unreadable: map[string]bool{}}
	var outcomes []fileOutcome
	for _, e := range entries {
		name := e.Name()
		if !qti.SafeEntryName(name) {
			res.Fail(qti.OutcomeInvalid, qti.Finding{
				Code: qti.CodeUnsafePath, File: name,
				Message: "entry name is absolute or leaves the package root",
			})
			continue
		}
		if e.IsDir() {
			continue
		}
		contents.Files = append(contents.Files, name)
		if !strings.EqualFold(path.Ext(name), ".xml") {
			continue
		}
		if name == qti.ManifestName {
			hasManifest = true
		}
		if stop, stopped := stopFinding(ctx.Err()); stopped {
			// Out of time or cancelled: the files from here on are not
			// validated, and the package references are not checked, as they
			// would miss files.
			stop.Message += "; " + name + " and the files after it were not validated"
			res.Fail(qti.OutcomeTooComplex, stop)
			v.stopFragments(ctx, outcomes, &res, limits.MaxFileSize, req)
			return res
		}
		listed := manifest.resources[name]
		fr, outcome, doc := v.validateEntry(ctx, e, limits.MaxFileSize, &budget, req, listed.kind, nil)
		if outcome == qti.OutcomeTooLarge {
			res.Fail(qti.OutcomeTooLarge, qti.Finding{
				Code:    qti.CodeTooLarge,
				Message: fmt.Sprintf("XML in the package exceeds %d uncompressed bytes", limits.MaxUncompressedSize),
			})
			v.stopFragments(ctx, outcomes, &res, limits.MaxFileSize, req)
			return res
		}
		res.Files = append(res.Files, fr)
		outcomes = append(outcomes, fileOutcome{
			index: len(res.Files) - 1, entry: e, outcome: outcome,
			// A fragment is validated once every document was read, as they
			// say where it is included.
			fragment: notQTI(fr) && listed.kind == qti.ResourceFragment && name != qti.ManifestName,
			// It may still be a fragment that a document further on includes.
			undecided: notQTI(fr) && listed.kind != qti.ResourceFragment && name != qti.ManifestName,
			document:  listed.kind == qti.ResourceDocument && listed.href,
		})
		if doc != nil {
			contents.Documents[name] = *doc
		} else {
			contents.Unreadable[name] = true
		}
	}
	v.decide(ctx, outcomes, &res, &contents, limits.MaxFileSize, req)
	if v.references != nil {
		applyReferenceFindings(&res, qti.CheckReferences(contents))
	}
	if !hasManifest {
		res.Fail(qti.OutcomeInvalid, qti.Finding{Code: qti.CodeMissingManifest, Message: "package has no " + qti.ManifestName + " at its root"})
	}
	res.Valid = res.Outcome == qti.OutcomeValid
	return res
}

// fileOutcome is the outcome of an XML file of a package. A fragment is not
// a QTI document, and the manifest lists it as a fragment resource. An
// undecided file is not a QTI document either, and the manifest does not
// call it a fragment: what it is depends on the documents of the package, as
// one that includes it with XInclude makes it a fragment.
type fileOutcome struct {
	index     int // in the package's files
	entry     ArchiveEntry
	outcome   qti.Outcome // as what the manifest says it is
	fragment  bool
	undecided bool
	// document reports that the file is the href of a QTI resource that is
	// no fragment: the manifest says it is a QTI document.
	document bool
}

// decide validates the fragments of the package: the files of its fragment
// resources, and the undecided files that a document of the package
// includes. It adds the outcome of every file to the package's. A fragment
// is validated where the documents of the package include it, which is known
// only now that all of them were read. Out of time, the files from there on
// are not validated as fragments: each says so itself, and a finding of the
// package names them.
func (v *Validator) decide(ctx context.Context, outcomes []fileOutcome, res *qti.PackageResult,
	contents *qti.PackageContents, maxSize int64, req qti.VersionRequest,
) {
	included := qti.Inclusions(*contents)
	stopped := false
	for _, u := range outcomes {
		name := u.entry.Name()
		places, isIncluded := included[name]
		if u.fragment || u.undecided && isIncluded {
			if stop, ok := stopFinding(ctx.Err()); ok && !stopped {
				stopped = true
				stop.Message += "; " + name + " and the QTI fragments after it were not validated"
				res.Fail(qti.OutcomeTooComplex, stop)
			}
			// Read once more, now as a fragment. The bytes were counted
			// against the package's budget the first time.
			again := maxSize
			fr, outcome, doc := v.validateEntry(ctx, u.entry, maxSize, &again, req, qti.ResourceFragment, &fragmentPlaces{included: places})
			switch {
			case !u.undecided || !notQTI(fr):
			case u.document:
				// The document of a QTI resource that is not QTI 3, such as
				// a QTI 2 item: that another document includes it does not
				// make it less of an error. Its first result says what it is.
				fr, outcome, doc = res.Files[u.index], u.outcome, nil
			default:
				// Included XML outside the QTI namespace, such as MathML, is
				// not QTI content. It is skipped, also when the manifest
				// lists it as a file of a QTI resource; only as the document
				// of a QTI resource, or as a fragment resource, it is an
				// error.
				fr.Valid, fr.Skipped, outcome = true, true, qti.OutcomeValid
			}
			res.Files[u.index], u.outcome = fr, outcome
			if doc != nil {
				contents.Documents[name] = *doc
			}
		}
		res.Outcome = res.Outcome.Worse(u.outcome)
	}
}

// stopFragments gives the fragment resources read so far their result as
// fragments when a package stops before decide validated them, out of time
// or over its size: until then their result is that of a file that is no
// QTI document. Out of time, that result says they were not validated.
func (v *Validator) stopFragments(ctx context.Context, outcomes []fileOutcome, res *qti.PackageResult, maxSize int64, req qti.VersionRequest) {
	for _, u := range outcomes {
		if u.fragment {
			again := maxSize
			res.Files[u.index], _, _ = v.validateEntry(ctx, u.entry, maxSize, &again, req, qti.ResourceFragment, &fragmentPlaces{})
		}
	}
}

// notQTI reports whether the result of a file is that of well-formed XML
// that is not a QTI document: skipped, or an error where the manifest lists
// it as one.
func notQTI(fr qti.FileResult) bool {
	return fr.Skipped || len(fr.Errors) == 1 && fr.Errors[0].Code == qti.CodeUnsupportedDocument
}

// manifestInfo is what the package needs from its manifest before the
// documents are validated.
type manifestInfo struct {
	// version is the version the manifest declares when it is a supported
	// one, and otherwise the latest. The manifest's own validation reports
	// an unsupported version.
	version string
	// resources are the files of the manifest's QTI resources. Such a file
	// must be a QTI document, or a QTI fragment; any other XML file that is
	// not a QTI document is skipped, unless a document of the package
	// includes it with XInclude, which makes it a fragment too.
	resources map[string]manifestFile
}

func readManifest(entries []ArchiveEntry, maxSize int64) manifestInfo {
	info := manifestInfo{version: qti.LatestVersion().Name, resources: map[string]manifestFile{}}
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
		info.resources = manifestResources(data)
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

// validateEntry validates one XML entry, and returns what it refers to when
// it could be read. Its outcome is OutcomeTooLarge only when the
// package-wide budget ran out, which aborts the package. The manifest and a
// file the manifest lists as a QTI resource are never skipped: if one is not
// what it must be, that is an error. With places, a file that is not a QTI
// document itself is checked as a QTI fragment that is included there;
// without, it is not checked as a fragment yet.
func (v *Validator) validateEntry(ctx context.Context, e ArchiveEntry, maxSize int64, budget *int64,
	req qti.VersionRequest, kind qti.ResourceKind, places *fragmentPlaces,
) (qti.FileResult, qti.Outcome, *qti.PackageDocument) {
	name := e.Name()
	out := qti.FileResult{File: name}
	if exceeds(e.DeclaredSize(), maxSize) {
		out.Errors = []qti.Finding{{Code: qti.CodeTooLarge, File: name, Message: fmt.Sprintf("file is larger than %d bytes", maxSize)}}
		return out, qti.OutcomeInvalid, nil
	}
	rc, err := e.Open()
	if err != nil {
		out.Errors = []qti.Finding{{Code: qti.CodeInvalidZIP, File: name, Message: err.Error()}}
		return out, qti.OutcomeInvalid, nil
	}
	defer rc.Close()

	// The declared size above is only a hint; the limit on the bytes actually
	// inflated is what stops a ZIP bomb.
	readLimit := min(maxSize, *budget)
	// The memory an entry takes is reserved for that entry alone: entries
	// are validated one after the other.
	defer releaseMemory(ctx)
	if f, outcome, ok := reserveToRead(ctx, int64(min(e.DeclaredSize(), uint64(max(readLimit, 0))))); !ok { //nolint:gosec // at most readLimit
		f.File = name
		out.Errors = []qti.Finding{f}
		if outcome == qti.OutcomeTooLarge {
			outcome = qti.OutcomeInvalid // too large for this service: an error of the file, the package carries on
		}
		return out, outcome, nil
	}
	counted := &countingReader{r: rc}
	limits := v.limits
	limits.MaxDocumentSize = readLimit
	// The entry can be read again: a validation that waits for memory lets
	// go of it meanwhile. The bytes count against the package once.
	src := &source{reread: func() ([]byte, *qti.DocumentResult) {
		again, err := e.Open()
		if err != nil {
			res := qti.Failure(qti.OutcomeInternal, "", qti.CodeInternal, "read: "+err.Error())
			return nil, &res
		}
		defer again.Close()
		return readDocument(ctx, again, limits)
	}}
	var failure *qti.DocumentResult
	src.data, failure = readDocument(ctx, counted, limits)
	var res qti.DocumentResult
	budgetRanOut := false
	if failure != nil {
		res = *failure
		budgetRanOut = res.Outcome == qti.OutcomeTooLarge && readLimit < maxSize
	} else {
		res = v.validate(ctx, src, req, limits)
		switch {
		case src.data == nil: // it could not be read again
		case name == qti.ManifestName:
			res = v.requireManifest(src.data, res)
		case places != nil && unsupportedDocument(res):
			res = v.validateFragment(ctx, src.data, req, limits, places.included)
		}
	}
	*budget -= counted.n
	doc := v.documentReferences(src.data, res)
	if doc != nil && name == qti.ManifestName && unsupportedDocument(res) {
		doc.Refs = qti.DocumentReferences{} // not a manifest: what it refers to instead is not the package's
	}

	out.Schema, out.Version, out.Ran = res.Schema, res.Version, res.Ran
	out.Errors, out.Warnings = inFile(res.Errors, name), inFile(res.Warnings, name)
	switch {
	case counted.err != nil:
		// A corrupt entry, a CRC mismatch, or more data than the header
		// declares: a ZIP bomb with a lying header.
		out.Schema = ""
		out.Errors = []qti.Finding{{Code: qti.CodeInvalidZIP, File: name, Message: counted.err.Error()}}
		return out, qti.OutcomeInvalid, nil
	case res.Outcome == qti.OutcomeValid:
		out.Valid = true
		return out, qti.OutcomeValid, doc
	case budgetRanOut:
		return out, qti.OutcomeTooLarge, nil
	case unsupportedDocument(res) && kind == qti.ResourceOther && name != qti.ManifestName:
		out.Valid, out.Skipped = true, true
		return out, qti.OutcomeValid, doc
	case res.Outcome == qti.OutcomeInternal, res.Outcome == qti.OutcomeTooComplex:
		return out, res.Outcome, doc
	}
	// Inside a package a malformed or oversized document makes the package
	// invalid; the request itself was fine.
	return out, qti.OutcomeInvalid, doc
}

// documentReferences reads what a validated document refers to. A document
// that could not be read in full refers to nothing; when its type is known
// it still counts as a file of that type, so references to it are checked.
// When reading failed before its type was known (too large, not
// well-formed, not UTF-8, a DOCTYPE), it returns nil: the file is
// unreadable, and references to it are not checked for their type.
func (v *Validator) documentReferences(data []byte, res qti.DocumentResult) *qti.PackageDocument {
	if v.references == nil || data == nil || res.Schema == "" && !unsupportedDocument(res) {
		return nil
	}
	doc := &qti.PackageDocument{Schema: res.Schema}
	if res.Outcome == qti.OutcomeMalformed {
		return doc
	}
	refs, err := v.references.ReadReferences(data)
	if err == nil {
		doc.Refs = refs
	}
	return doc
}

// applyReferenceFindings adds the findings of the reference checks to the
// files they are about, or to the package when that file was not validated
// as a QTI document, such as an image.
func applyReferenceFindings(res *qti.PackageResult, findings []qti.ReferenceFinding) {
	files := map[string]*qti.FileResult{}
	for i := range res.Files {
		if !res.Files[i].Skipped {
			files[res.Files[i].File] = &res.Files[i]
		}
	}
	for _, f := range findings {
		fr := files[f.File]
		switch {
		case fr == nil && f.Warning:
			res.Warnings = append(res.Warnings, f.Finding)
		case fr == nil:
			res.Fail(qti.OutcomeInvalid, f.Finding)
		case f.Warning:
			fr.Warnings = append(fr.Warnings, f.Finding)
		default:
			fr.Errors = append(fr.Errors, f.Finding)
			fr.Valid = false
			res.Outcome = res.Outcome.Worse(qti.OutcomeInvalid)
		}
	}
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
