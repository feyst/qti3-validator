package app

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"

	"qti3-validator/internal/domain/qti"
	"qti3-validator/internal/lib/xmldoc"
)

// ValidateDocument validates one XML document: against the XML Schema of
// its type and QTI version, then against the rules of that profile. A
// document type a mounted validator adds is checked against that
// validator's profile and has no QTI version. The document is held in
// memory, bounded by the MaxDocumentSize limit.
//
// A document that could not be read far enough to know its type, such as
// malformed XML, gets the QTI version it would have been validated against:
// the forced one or the latest.
func (v *Validator) ValidateDocument(ctx context.Context, q ValidateDocument) qti.DocumentResult {
	req := qti.VersionRequest{Forced: q.Version}
	res := v.validateReader(ctx, q.Document, q.Size, req, v.limits)
	if res.Version == "" && res.Schema == "" {
		res.Version = qti.ChooseVersion(req, qti.DeclaredVersion{}).Version.Name
	}
	return res
}

// size is the size of the document when it is known, else 0: the memory to
// read it is then reserved for the largest document allowed.
func (v *Validator) validateReader(ctx context.Context, r io.Reader, size int64, req qti.VersionRequest, limits qti.Limits) qti.DocumentResult {
	known := size // 0 when the size is not known, or more than is read
	if size <= 0 || size > limits.MaxDocumentSize {
		size, known = limits.MaxDocumentSize, 0
	}
	if f, outcome, ok := reserveToRead(ctx, size); !ok {
		return qti.DocumentResult{Errors: []qti.Finding{f}, Outcome: outcome}
	}
	data, failure := readDocument(ctx, r, known, limits)
	if failure != nil {
		return *failure
	}
	src := &source{data: data}
	if seeker, ok := r.(io.Seeker); ok {
		src.reread = func() ([]byte, *qti.DocumentResult) {
			if _, err := seeker.Seek(0, io.SeekStart); err != nil {
				res := qti.Failure(qti.OutcomeInternal, "", qti.CodeInternal, "read: "+err.Error())
				return nil, &res
			}
			return readDocument(ctx, r, known, limits)
		}
	}
	return v.validate(ctx, src, req, limits)
}

// source is a document in memory, and the way to read it again when there
// is one: a validation that must wait for memory keeps nothing while it
// waits, not even its document (see reserveForRules).
type source struct {
	data   []byte
	reread func() ([]byte, *qti.DocumentResult)
}

// readDocument reads a document into memory, bounded by MaxDocumentSize. On
// failure it returns the result to report instead. size is the size of the
// document when the caller knows it, else 0: the document is then read into
// a buffer of that size at once, instead of one that grows to hold it.
func readDocument(ctx context.Context, r io.Reader, size int64, limits qti.Limits) ([]byte, *qti.DocumentResult) {
	lr := &limitReader{r: ctxReader{ctx: ctx, r: r}, remaining: limits.MaxDocumentSize}
	data, err := readAll(lr, size)
	var res qti.DocumentResult
	stop, stopped := stopFinding(ctx.Err())
	switch {
	case lr.exceeded:
		res = qti.Failure(qti.OutcomeTooLarge, "", qti.CodeTooLarge,
			fmt.Sprintf("document is larger than %d bytes", limits.MaxDocumentSize))
	case stopped:
		// Out of time or cancelled while reading: stopped like a validation
		// that runs out of time, not an internal error.
		res = qti.Failure(qti.OutcomeTooComplex, "", stop.Code, stop.Message)
	case err != nil:
		res = qti.Failure(qti.OutcomeInternal, "", qti.CodeInternal, "read: "+err.Error())
	default:
		return data, nil
	}
	return nil, &res
}

// readAll reads r to its end like io.ReadAll, into a buffer for size bytes
// when size is positive. A document of another size is read as well.
func readAll(r io.Reader, size int64) ([]byte, error) {
	if size <= 0 {
		return io.ReadAll(r)
	}
	// One byte more is the room for the read that finds the end.
	b := make([]byte, 0, size+1)
	for {
		if len(b) == cap(b) {
			b = append(b, 0)[:len(b)]
		}
		n, err := r.Read(b[len(b):cap(b)])
		b = b[:len(b)+n]
		if errors.Is(err, io.EOF) {
			return b, nil
		}
		if err != nil {
			return b, err
		}
	}
}

// validate validates a document in memory. When ctx is done or the rules
// need too much work, it stops with a too_complex finding after the findings
// so far.
func (v *Validator) validate(ctx context.Context, src *source, req qti.VersionRequest, limits qti.Limits) qti.DocumentResult {
	root, err := xmldoc.DetectRoot(bytes.NewReader(src.data))
	if err != nil {
		return detectionFailure(err)
	}
	docType, ok := qti.LookupDocumentType(root)
	if !ok {
		if p, ok := v.custom[root]; ok {
			return v.validateCustom(ctx, v.customTypes[root], p, src, limits)
		}
		// Malformed XML is reported as such, whatever its root.
		// The check is bound like the checks of a supported document: by
		// the nesting depth and by ctx.
		if err := xmldoc.CheckWellFormed(ctx, bytes.NewReader(src.data), limits.MaxDepth); err != nil {
			return detectionFailure(err)
		}
		return qti.Failure(qti.OutcomeInvalid, "", qti.CodeUnsupportedDocument,
			fmt.Sprintf("root element {%s}%s is not a supported QTI 3 document", root.Space, root.Local))
	}

	var declared qti.DeclaredVersion
	if docType.Namespace == qti.NamespaceManifest {
		declared = manifestSchemaVersion(src.data)
	}
	choice := qti.ChooseVersion(req, declared)
	profile := v.versions[choice.Version.Name]

	if f, outcome, ok := reserveToCheck(ctx, profile.Rules, src); !ok {
		return qti.DocumentResult{Schema: docType.Schema, Version: choice.Version.Name, Errors: []qti.Finding{f}, Outcome: outcome}
	}
	res, verdict := checkSchema(ctx, profile.Schema, docType, src.data, limits)
	if choice.ReplacesSchemaVerdict {
		res.DropSchemaVersionVerdict()
	}
	// Decided after the version verdict is dropped: a manifest whose only
	// error was its schemaversion is assessed in full.
	assessed := assessable(res, verdict)
	res.Version = choice.Version.Name
	choice.ApplyNotice(&res)
	if !assessed {
		return res // not well-formed, too large, not assessable, or stopped
	}
	return checkRules(ctx, profile.Rules, src, res, limits)
}

func (v *Validator) validateCustom(ctx context.Context, docType qti.DocumentType, p Profile, src *source, limits qti.Limits) qti.DocumentResult {
	if f, outcome, ok := reserveToCheck(ctx, p.Rules, src); !ok {
		return qti.DocumentResult{Schema: docType.Schema, Errors: []qti.Finding{f}, Outcome: outcome}
	}
	res, verdict := checkSchema(ctx, p.Schema, docType, src.data, limits)
	if !assessable(res, verdict) {
		return res
	}
	return checkRules(ctx, p.Rules, src, res, limits)
}

// checkSchema runs the schema check. The check itself cannot be
// interrupted, so ctx is checked before and after it.
func checkSchema(ctx context.Context, s SchemaChecker, docType qti.DocumentType, data []byte, limits qti.Limits) (qti.DocumentResult, SchemaVerdict) {
	if f, ok := stopFinding(ctx.Err()); ok {
		res := qti.DocumentResult{Schema: docType.Schema}
		res.AddError(f, qti.OutcomeTooComplex)
		return res, SchemaVerdict{}
	}
	verdict := s.CheckSchema(data, limits)
	res := qti.DocumentResult{
		Valid:   verdict.Outcome == qti.OutcomeValid,
		Schema:  docType.Schema,
		Errors:  verdict.Findings,
		Outcome: verdict.Outcome,
		Ran:     qti.Checks{Schema: true},
	}
	if f, ok := stopFinding(ctx.Err()); ok {
		res.AddError(f, qti.OutcomeTooComplex)
	}
	return res, verdict
}

// assessable reports whether the rules can run after the schema check: the
// document is valid, or invalid with the whole document assessed.
func assessable(res qti.DocumentResult, verdict SchemaVerdict) bool {
	return res.Outcome == qti.OutcomeValid || res.Outcome == qti.OutcomeInvalid && verdict.Conclusive
}

// checkRules runs the rules on a well-formed document and adds their
// findings to res, as long as fewer than MaxErrors errors were found. It
// first reserves the memory they take for this document (see Memory); a
// document that needs more than a validation may take gets a too_large
// finding that says so, and its rules do not run. The
// rules may add errors up to MaxErrors in all, and MaxErrors warnings of
// their own; warnings do not take the room of errors. Rules that stop before
// the end add the findings so far and a too_complex finding; rules that fail
// add the findings so far, of the rule sets before and of their own, and an
// internal error.
func checkRules(ctx context.Context, rules RuleChecker, src *source, res qti.DocumentResult, limits qti.Limits) qti.DocumentResult {
	if rules == nil {
		return res
	}
	remaining := limits.MaxErrors - len(res.Errors)
	if remaining <= 0 {
		return res
	}
	if f, outcome, ok := reserveForRules(ctx, rules, src); !ok {
		// The rules did not run: the document has no room in memory, now
		// or ever.
		res.AddError(f, outcome)
		return res
	}
	res.Ran.Rules = true
	findings, err := rules.CheckRules(ctx, src.data, RuleLimits{Errors: remaining, Warnings: limits.MaxErrors})
	for _, f := range findings {
		if f.Warning {
			res.Warnings = append(res.Warnings, f.Finding)
			continue
		}
		res.AddError(f.Finding, qti.OutcomeInvalid)
	}
	switch stop, stopped := stopFinding(err); {
	case errors.Is(err, ErrTooLarge):
		res.AddError(qti.Finding{Code: qti.CodeTooLarge, Message: "the document has more nodes than the rules can check"}, qti.OutcomeTooLarge)
	case stopped:
		res.AddError(stop, qti.OutcomeTooComplex)
	case err != nil:
		// Like a validation that stops: what was found before is kept.
		res.AddError(qti.Finding{Code: qti.CodeInternal, Message: err.Error()}, qti.OutcomeInternal)
	}
	return res
}

// stopFinding returns the finding for a validation that err stopped before
// the end: the document needs more work than the checks allow, or ctx is
// done, normally because the request ran out of time. It reports false for
// any other error.
func stopFinding(err error) (qti.Finding, bool) {
	var msg string
	switch {
	case errors.Is(err, ErrTooComplex):
		msg = "validation stopped: the document is too complex to validate"
	case errors.Is(err, ErrMemoryBusy):
		msg = "validation stopped: the service had no memory free to check the document in time"
	case errors.Is(err, context.DeadlineExceeded):
		msg = "validation stopped: the document took longer to validate than REQUEST_TIMEOUT allows"
	case errors.Is(err, context.Canceled):
		msg = "validation stopped: the request was cancelled"
	default:
		return qti.Finding{}, false
	}
	return qti.Finding{Code: qti.CodeTooComplex, Message: msg}, true
}

// detectionFailure is the result of a document whose root cannot be read.
func detectionFailure(err error) qti.DocumentResult {
	switch {
	case errors.Is(err, xmldoc.ErrUnsupportedEncoding):
		return qti.Failure(qti.OutcomeInvalid, "", qti.CodeUnsupportedEncoding, err.Error())
	case errors.Is(err, xmldoc.ErrDTD):
		return qti.Failure(qti.OutcomeInvalid, "", qti.CodeUnsupportedXML, err.Error())
	}
	if stop, ok := stopFinding(err); ok {
		return qti.Failure(qti.OutcomeTooComplex, "", stop.Code, stop.Message)
	}
	if deep, ok := errors.AsType[*xmldoc.DepthError](err); ok {
		// As the schema check reports it for a supported document.
		res := qti.Failure(qti.OutcomeInvalid, "", qti.CodeLimitExceeded,
			fmt.Sprintf("elements are nested deeper than %d (MAX_DEPTH)", deep.Max))
		res.Errors[0].Line = deep.Line
		return res
	}
	res := qti.Failure(qti.OutcomeMalformed, "", qti.CodeInvalidXML, err.Error())
	if syntax, ok := errors.AsType[*xml.SyntaxError](err); ok {
		res.Errors[0].Line = syntax.Line
		res.Errors[0].Message = syntax.Msg
	}
	return res
}

// limitReader fails once more than remaining bytes are read, and remembers
// that it did, so callers can tell a size violation from malformed input.
type limitReader struct {
	r         io.Reader
	remaining int64
	exceeded  bool
}

var errTooLarge = errors.New("input exceeds the size limit")

func (l *limitReader) Read(p []byte) (int, error) {
	if l.exceeded {
		return 0, errTooLarge
	}
	if int64(len(p)) > l.remaining+1 {
		p = p[:l.remaining+1]
	}
	n, err := l.r.Read(p)
	if int64(n) > l.remaining {
		l.exceeded = true
		return int(l.remaining), errTooLarge
	}
	l.remaining -= int64(n)
	return n, err
}

// ctxReader stops reading once the context is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
