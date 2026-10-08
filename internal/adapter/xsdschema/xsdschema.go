// Package xsdschema checks documents against XML Schemas with
// github.com/jacoelho/xsd, and implements app.SchemaChecker.
package xsdschema

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"

	"github.com/jacoelho/xsd"
	"github.com/jacoelho/xsd/xsderrors"

	"qti3-validator/internal/adapter/schemastore"
	"qti3-validator/internal/app"
	"qti3-validator/internal/domain/qti"
)

// Resolver resolves schema locations to the schema store. A missing schema
// is a hard error, not xsderrors.ErrSchemaNotFound: that would let
// compilation carry on without it and fail later on an unrelated reference,
// or not at all.
type Resolver struct {
	Store schemastore.Store
	// Loaded, if set, is called for every schema that is resolved.
	Loaded func(url string)
}

// Source returns the schema at schemaURL, named by that URL so relative
// includes and imports resolve against it.
func (r Resolver) Source(schemaURL string) (xsd.SchemaSource, error) {
	if err := r.Store.Has(schemaURL); err != nil {
		return xsd.SchemaSource{}, err
	}
	if r.Loaded != nil {
		r.Loaded(schemaURL)
	}
	return xsd.Open(schemaURL, func() (io.ReadCloser, error) {
		return r.Store.Open(schemaURL)
	}).WithResolver(r), nil
}

// ResolveSchema implements xsd.Resolver.
func (r Resolver) ResolveSchema(base, location string) (xsd.SchemaSource, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return xsd.SchemaSource{}, err
	}
	ref, err := url.Parse(location)
	if err != nil {
		return xsd.SchemaSource{}, err
	}
	return r.Source(baseURL.ResolveReference(ref).String())
}

// Checker is a compiled schema set. It is safe for concurrent use.
type Checker struct {
	engine *xsd.Engine
	// source names a mounted XSD; its findings carry it.
	source string
}

var _ app.SchemaChecker = (*Checker)(nil)

// Compile compiles a schema set from its entry schemas.
func Compile(sources ...xsd.SchemaSource) (*Checker, error) {
	engine, err := xsd.Compile(sources...)
	if err != nil {
		return nil, err
	}
	return &Checker{engine: engine}, nil
}

// CompileVersion compiles the schemas of a QTI version.
func CompileVersion(r Resolver, v qti.Version) (*Checker, error) {
	var sources []xsd.SchemaSource
	for _, u := range []string{v.ASI, v.Manifest} {
		src, err := r.Source(u)
		if err != nil {
			return nil, err
		}
		sources = append(sources, src)
	}
	c, err := Compile(sources...)
	if err != nil {
		return nil, fmt.Errorf("compile QTI %s schemas: %w", v.Name, err)
	}
	return c, nil
}

// WithSource returns a checker whose findings name source, a file in the
// validators directory.
func (c *Checker) WithSource(source string) *Checker {
	return &Checker{engine: c.engine, source: source}
}

// CheckSchema implements app.SchemaChecker.
func (c *Checker) CheckSchema(data []byte, limits qti.Limits) app.SchemaVerdict {
	err := c.engine.ValidateWithOptions(bytes.NewReader(data), xsd.ValidateOptions{
		MaxErrors:        limits.MaxErrors,
		MaxInstanceDepth: limits.MaxDepth,
		MaxInstanceBytes: limits.MaxDocumentSize,
	})
	if err == nil {
		return app.SchemaVerdict{Outcome: qti.OutcomeValid, Conclusive: true}
	}
	v := verdictFor(err)
	if c.source != "" {
		for i := range v.Findings {
			if v.Findings[i].Code == qti.CodeValidation {
				v.Findings[i].Source = c.source
			}
		}
	}
	return v
}

// verdictFor converts a validation error of the XSD library into findings.
// The most severe diagnostic decides the outcome.
func verdictFor(err error) app.SchemaVerdict {
	v := app.SchemaVerdict{Outcome: qti.OutcomeInvalid, Conclusive: xsderrors.IsConclusiveValidation(err)}
	for _, e := range xsderrors.Flatten(err) {
		diag, ok := errors.AsType[*xsderrors.Error](e)
		if !ok {
			v.Findings = append(v.Findings, qti.Finding{Code: qti.CodeInternal, Message: e.Error()})
			v.Outcome = v.Outcome.Worse(qti.OutcomeInternal)
			continue
		}
		code, outcome := classify(diag)
		message := diag.Message()
		if message == "" && diag.Cause() != nil {
			// Parse errors carry their text in the cause.
			message = diag.Cause().Error()
		}
		v.Findings = append(v.Findings, qti.Finding{
			Code:    code,
			Line:    diag.Line(),
			Column:  diag.Column(),
			Path:    diag.Path(),
			Message: message,
		})
		v.Outcome = v.Outcome.Worse(outcome)
	}
	if len(v.Findings) == 0 {
		v.Findings = []qti.Finding{{Code: qti.CodeInternal, Message: err.Error()}}
		v.Outcome = qti.OutcomeInternal
	}
	return v
}

func classify(diag *xsderrors.Error) (qti.Code, qti.Outcome) {
	switch diag.Code() {
	case xsderrors.CodeFormatXML, xsderrors.CodeValidationXML:
		return qti.CodeInvalidXML, qti.OutcomeMalformed
	case xsderrors.CodeUnsupportedNonUTF8:
		return qti.CodeUnsupportedEncoding, qti.OutcomeInvalid
	case xsderrors.CodeValidationLimit, xsderrors.CodeFormatLimit:
		return qti.CodeLimitExceeded, qti.OutcomeInvalid
	case xsderrors.CodeValidationSession, xsderrors.CodeValidationOption, xsderrors.CodeFormatOption:
		return qti.CodeInternal, qti.OutcomeInternal
	}
	switch diag.Category() {
	case xsderrors.CategoryValidation:
		return qti.CodeValidation, qti.OutcomeInvalid
	case xsderrors.CategoryUnsupported:
		return qti.CodeUnsupportedXML, qti.OutcomeInvalid
	}
	return qti.CodeInternal, qti.OutcomeInternal
}
