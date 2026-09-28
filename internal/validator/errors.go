package validator

import (
	"errors"

	"github.com/jacoelho/xsd/xsderrors"
)

// Outcome classifies a result; the HTTP layer maps it to a status code.
type Outcome int

const (
	OutcomeValid     Outcome = iota
	OutcomeInvalid           // well-formed XML that is not acceptable QTI
	OutcomeMalformed         // not well-formed XML, or not a readable ZIP
	OutcomeTooLarge          // input exceeds a configured size limit
	OutcomeInternal          // validator failure
)

// Error codes in the API. They are our own and do not expose library codes.
const (
	CodeInvalidXML          = "invalid_xml"
	CodeUnsupportedEncoding = "unsupported_encoding"
	CodeUnsupportedXML      = "unsupported_xml"
	CodeUnsupportedDocument = "unsupported_document"
	CodeValidation          = "validation"
	CodeSchematron          = "schematron"
	CodeUnsupportedVersion  = "unsupported_version"
	CodeVersionOverridden   = "version_overridden"
	CodeLimitExceeded       = "limit_exceeded"
	CodeTooLarge            = "too_large"
	CodeInternal            = "internal"
	CodeInvalidZIP          = "invalid_zip"
	CodeUnsafePath          = "unsafe_path"
	CodeMissingManifest     = "missing_manifest"
	CodeTooManyFiles        = "too_many_files"
)

// ValidationResult is the result of validating one XML document.
type ValidationResult struct {
	Valid    bool              `json:"valid"`
	Schema   string            `json:"schema,omitempty"`
	Version  string            `json:"version,omitempty"` // QTI version validated against
	Errors   []ValidationError `json:"errors,omitempty"`
	Warnings []ValidationError `json:"warnings,omitempty"` // Schematron rules with a warning role, version overrides
	Outcome  Outcome           `json:"-"`
}

// ValidationError is one problem in a document or package.
type ValidationError struct {
	Code    string `json:"code"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Path    string `json:"path,omitempty"`
	Rule    string `json:"rule,omitempty"`   // Schematron pattern id, and assertion id if any
	Source  string `json:"source,omitempty"` // file in the validators directory the finding came from
	Message string `json:"message"`
}

func failure(outcome Outcome, schema, code, message string) ValidationResult {
	return ValidationResult{
		Schema:  schema,
		Errors:  []ValidationError{{Code: code, Message: message}},
		Outcome: outcome,
	}
}

// fromLibraryError converts a validation error of the XSD library into the
// API model. The most severe diagnostic decides the outcome.
func fromLibraryError(schema string, err error) ValidationResult {
	res := ValidationResult{Schema: schema, Outcome: OutcomeInvalid}
	for _, e := range xsderrors.Flatten(err) {
		var diag *xsderrors.Error
		if !errors.As(e, &diag) {
			res.Errors = append(res.Errors, ValidationError{Code: CodeInternal, Message: e.Error()})
			res.Outcome = worse(res.Outcome, OutcomeInternal)
			continue
		}
		code, outcome := classify(diag)
		message := diag.Message()
		if message == "" && diag.Cause() != nil {
			// Parse errors carry their text in the cause.
			message = diag.Cause().Error()
		}
		res.Errors = append(res.Errors, ValidationError{
			Code:    code,
			Line:    diag.Line(),
			Column:  diag.Column(),
			Path:    diag.Path(),
			Message: message,
		})
		res.Outcome = worse(res.Outcome, outcome)
	}
	if len(res.Errors) == 0 {
		res.Errors = []ValidationError{{Code: CodeInternal, Message: err.Error()}}
		res.Outcome = OutcomeInternal
	}
	return res
}

// xsderrorsConclusive reports whether the XSD library assessed the whole
// document, so that the Schematron rules can run on it too.
func xsderrorsConclusive(err error) bool {
	return xsderrors.IsConclusiveValidation(err)
}

func classify(diag *xsderrors.Error) (string, Outcome) {
	switch diag.Code() {
	case xsderrors.CodeFormatXML, xsderrors.CodeValidationXML:
		return CodeInvalidXML, OutcomeMalformed
	case xsderrors.CodeUnsupportedNonUTF8:
		return CodeUnsupportedEncoding, OutcomeInvalid
	case xsderrors.CodeValidationLimit, xsderrors.CodeFormatLimit:
		return CodeLimitExceeded, OutcomeInvalid
	case xsderrors.CodeValidationSession, xsderrors.CodeValidationOption, xsderrors.CodeFormatOption:
		return CodeInternal, OutcomeInternal
	}
	switch diag.Category() {
	case xsderrors.CategoryValidation:
		return CodeValidation, OutcomeInvalid
	case xsderrors.CategoryUnsupported:
		return CodeUnsupportedXML, OutcomeInvalid
	}
	return CodeInternal, OutcomeInternal
}

// worse orders outcomes by severity: internal > malformed > too large > invalid.
func worse(a, b Outcome) Outcome {
	rank := func(o Outcome) int {
		switch o {
		case OutcomeInternal:
			return 4
		case OutcomeMalformed:
			return 3
		case OutcomeTooLarge:
			return 2
		case OutcomeInvalid:
			return 1
		}
		return 0
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}
