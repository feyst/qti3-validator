package app

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"

	"github.com/kennisnet/qti3-validator/internal/domain/qti"
	"github.com/kennisnet/qti3-validator/internal/lib/xmldoc"
)

// ValidateDocument validates one XML document: against the XML Schema of
// its type and QTI version, then against the rules of that profile. A
// document type a mounted validator adds is checked against that
// validator's profile and has no QTI version. The document is held in
// memory, bounded by the MaxDocumentSize limit.
func (v *Validator) ValidateDocument(ctx context.Context, q ValidateDocument) qti.DocumentResult {
	return v.validateReader(ctx, q.Document, qti.VersionRequest{Forced: q.Version}, v.limits)
}

func (v *Validator) validateReader(ctx context.Context, r io.Reader, req qti.VersionRequest, limits qti.Limits) qti.DocumentResult {
	data, failure := readDocument(ctx, r, limits)
	if failure != nil {
		return *failure
	}
	return v.validate(data, req, limits)
}

// readDocument reads a document into memory, bounded by MaxDocumentSize. On
// failure it returns the result to report instead.
func readDocument(ctx context.Context, r io.Reader, limits qti.Limits) ([]byte, *qti.DocumentResult) {
	lr := &limitReader{r: ctxReader{ctx: ctx, r: r}, remaining: limits.MaxDocumentSize}
	data, err := io.ReadAll(lr)
	var res qti.DocumentResult
	switch {
	case lr.exceeded:
		res = qti.Failure(qti.OutcomeTooLarge, "", qti.CodeTooLarge,
			fmt.Sprintf("document is larger than %d bytes", limits.MaxDocumentSize))
	case ctx.Err() != nil:
		res = qti.Failure(qti.OutcomeInternal, "", qti.CodeInternal, "validation interrupted: "+ctx.Err().Error())
	case err != nil:
		res = qti.Failure(qti.OutcomeInternal, "", qti.CodeInternal, "read: "+err.Error())
	default:
		return data, nil
	}
	return nil, &res
}

func (v *Validator) validate(data []byte, req qti.VersionRequest, limits qti.Limits) qti.DocumentResult {
	root, err := xmldoc.DetectRoot(bytes.NewReader(data))
	if err != nil {
		return detectionFailure(err)
	}
	docType, ok := qti.LookupDocumentType(root)
	if !ok {
		if p, ok := v.custom[root]; ok {
			return v.validateCustom(v.customTypes[root], p, data, limits)
		}
		// Malformed XML is reported as such, whatever its root.
		if err := xmldoc.CheckWellFormed(bytes.NewReader(data)); err != nil {
			return detectionFailure(err)
		}
		return qti.Failure(qti.OutcomeInvalid, "", qti.CodeUnsupportedDocument,
			fmt.Sprintf("root element {%s}%s is not a supported QTI 3 document", root.Space, root.Local))
	}

	var declared qti.DeclaredVersion
	if docType.Namespace == qti.NamespaceManifest {
		declared = manifestSchemaVersion(data)
	}
	choice := qti.ChooseVersion(req, declared)
	profile := v.versions[choice.Version.Name]

	res, verdict := checkSchema(profile.Schema, docType, data, limits)
	if choice.ReplacesSchemaVerdict {
		res.DropSchemaVersionVerdict()
	}
	// Decided after the version verdict is dropped: a manifest whose only
	// error was its schemaversion is assessed in full.
	assessed := assessable(res, verdict)
	res.Version = choice.Version.Name
	choice.ApplyNotice(&res)
	if !assessed {
		return res // not well-formed, too large, or not assessable
	}
	return checkRules(profile.Rules, data, res, limits)
}

func (v *Validator) validateCustom(docType qti.DocumentType, p Profile, data []byte, limits qti.Limits) qti.DocumentResult {
	res, verdict := checkSchema(p.Schema, docType, data, limits)
	if !assessable(res, verdict) {
		return res
	}
	return checkRules(p.Rules, data, res, limits)
}

// checkSchema runs the schema check.
func checkSchema(s SchemaChecker, docType qti.DocumentType, data []byte, limits qti.Limits) (qti.DocumentResult, SchemaVerdict) {
	verdict := s.CheckSchema(data, limits)
	return qti.DocumentResult{
		Valid:   verdict.Outcome == qti.OutcomeValid,
		Schema:  docType.Schema,
		Errors:  verdict.Findings,
		Outcome: verdict.Outcome,
	}, verdict
}

// assessable reports whether the rules can run after the schema check: the
// document is valid, or invalid with the whole document assessed.
func assessable(res qti.DocumentResult, verdict SchemaVerdict) bool {
	return res.Outcome == qti.OutcomeValid || res.Outcome == qti.OutcomeInvalid && verdict.Conclusive
}

// checkRules runs the rules on a well-formed document and adds their
// findings to res, as long as fewer than MaxErrors errors were found.
func checkRules(rules RuleChecker, data []byte, res qti.DocumentResult, limits qti.Limits) qti.DocumentResult {
	if rules == nil {
		return res
	}
	remaining := limits.MaxErrors - len(res.Errors)
	if remaining <= 0 {
		return res
	}
	findings, err := rules.CheckRules(data, remaining)
	if err != nil {
		return qti.Failure(qti.OutcomeInternal, res.Schema, qti.CodeInternal, err.Error())
	}
	for _, f := range findings {
		if f.Warning {
			res.Warnings = append(res.Warnings, f.Finding)
			continue
		}
		res.AddError(f.Finding, qti.OutcomeInvalid)
	}
	return res
}

// detectionFailure is the result of a document whose root cannot be read.
func detectionFailure(err error) qti.DocumentResult {
	switch {
	case errors.Is(err, xmldoc.ErrUnsupportedEncoding):
		return qti.Failure(qti.OutcomeInvalid, "", qti.CodeUnsupportedEncoding, err.Error())
	case errors.Is(err, xmldoc.ErrDTD):
		return qti.Failure(qti.OutcomeInvalid, "", qti.CodeUnsupportedXML, err.Error())
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
