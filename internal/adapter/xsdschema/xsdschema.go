// Package xsdschema checks documents against XML Schemas with
// github.com/jacoelho/xsd, and implements app.SchemaChecker.
package xsdschema

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"strings"

	"github.com/jacoelho/xsd"
	"github.com/jacoelho/xsd/xsderrors"

	"qti3-validator/internal/adapter/schemastore"
	"qti3-validator/internal/app"
	"qti3-validator/internal/domain/qti"
	"qti3-validator/internal/lib/xsdelements"
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
		if diag.Code() == xsderrors.CodeValidationRoot {
			v.RootUndeclared = true
		}
		// The library has no code of its own for an IDREF without its ID:
		// it is a validation.type error like any other, told apart by the
		// start of its message (app's TestFragmentUnresolvedIDRef pins it).
		if diag.Code() == xsderrors.CodeValidationType && strings.HasPrefix(diag.Message(), unresolvedIDRef) {
			v.UnresolvedIDRefs = append(v.UnresolvedIDRefs, len(v.Findings))
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

// unresolvedIDRef starts the library's message for an IDREF whose ID is not
// in the document. The library reports these at the end of the document,
// after every other error, so the ones a fragment drops never take the room
// of another error under MaxErrors.
const unresolvedIDRef = "IDREF does not resolve"

// DeclaredElements reads the names of the elements the schema at schemaURL
// declares in its target namespace: at the top level, and locally inside
// its types and groups, and which of them it declares with more than one
// type. Elements of imported schemas, which are in other namespaces, are not
// part of it.
//
// It reads the whole schema, which takes about as long as compiling it. For
// the embedded QTI schemas that is done at build time, by cmd/fetchschemas
// with the same xsdelements.Read: the service loads the result with
// CompiledElements.
func DeclaredElements(r Resolver, schemaURL string) (app.SchemaElements, error) {
	f, err := r.Store.Open(schemaURL)
	if err != nil {
		return app.SchemaElements{}, err
	}
	defer f.Close()
	read, err := xsdelements.Read(f)
	if err != nil {
		return app.SchemaElements{}, fmt.Errorf("read the elements of %s: %w", schemaURL, err)
	}
	return schemaElements(read), nil
}

// CompiledElements returns the elements of the schema at schemaURL as they
// were read at build time.
func CompiledElements(c *xsdelements.Compiled, schemaURL string) (app.SchemaElements, error) {
	schema, ok := c.Schemas[schemaURL]
	if !ok {
		return app.SchemaElements{}, fmt.Errorf(
			"the elements of %s are not compiled in: run `go run ./cmd/fetchschemas` again", schemaURL)
	}
	return schemaElements(schema.Elements), nil
}

// VerifyElements fails when the elements compiled in at build time are not
// those of the schema at schemaURL as it is stored: when the checksum they
// were compiled with is not that of the schema, or when reading the schema
// gives other elements. They were then read from another schema, or by
// another version of the code.
//
// It reads the schema twice, which takes longer than compiling it: for the
// check of the image build (qti-validator -check). The service itself
// starts without it; the checksum alone would add 60 ms to a start, for
// the two QTI versions.
func VerifyElements(r Resolver, c *xsdelements.Compiled, schemaURL string) error {
	compiled, err := CompiledElements(c, schemaURL)
	if err != nil {
		return err
	}
	sum, err := r.Store.SHA256(schemaURL)
	if err != nil {
		return err
	}
	if from := c.Schemas[schemaURL].SHA256; from != sum {
		return fmt.Errorf(
			"the compiled elements of %s were read from a schema with checksum %q, and the schema has %s: run `go run ./cmd/fetchschemas` again",
			schemaURL, from, sum)
	}
	declared, err := DeclaredElements(r, schemaURL)
	if err != nil {
		return err
	}
	if compiled.Namespace != declared.Namespace || !maps.Equal(compiled.Global, declared.Global) ||
		!maps.Equal(compiled.Local, declared.Local) || !maps.Equal(compiled.ManyTyped, declared.ManyTyped) {
		return fmt.Errorf(
			"the compiled elements of %s are not those the schema declares: run `go run ./cmd/fetchschemas` again", schemaURL)
	}
	return nil
}

func schemaElements(e xsdelements.Elements) app.SchemaElements {
	return app.SchemaElements{Namespace: e.Namespace, Global: set(e.Global), Local: set(e.Local), ManyTyped: set(e.ManyTyped)}
}

func set(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = true
	}
	return out
}
