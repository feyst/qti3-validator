// Package validator validates QTI 3 XML documents and packages against the
// embedded QTI 3 XSDs.
//
// It checks well-formedness, XSD validity, and the Schematron rules that
// 1EdTech embeds in the XSDs.
package validator

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"sync"

	"github.com/jacoelho/xsd"
	"github.com/kennisnet/qti3-validator/internal/schematron"
	"github.com/kennisnet/qti3-validator/internal/xpath"
)

// Limits bound the work per document.
type Limits struct {
	MaxDocumentSize int64 // bytes per XML document
	MaxErrors       int   // validation errors reported per document
	MaxDepth        int   // nesting depth of XML elements
}

// DefaultLimits are used for zero fields in Options.Limits.
var DefaultLimits = Limits{
	MaxDocumentSize: 10 << 20,
	MaxErrors:       100,
	MaxDepth:        256,
}

// Options configure New.
type Options struct {
	// FS holds the schemas, laid out as URL host and path. Nil selects the
	// embedded schemas.
	FS fs.FS
	// Limits apply to every document; zero fields select DefaultLimits.
	Limits Limits
	// OnSchemaLoaded is called once for every schema file that is loaded.
	OnSchemaLoaded func(url string)
	// ValidatorsDir is a directory with extra validators, .xsd and .sch
	// files; see mounted.go. Empty adds none.
	ValidatorsDir string
	// OnValidatorLoaded is called for every file loaded from
	// ValidatorsDir, with the roots of the document types an XSD adds.
	OnValidatorLoaded func(file string, roots []string)
	// OnValidatorIgnored is called for every entry of ValidatorsDir that
	// is not used.
	OnValidatorIgnored func(file, reason string)
}

// Validator holds the compiled schemas of every supported QTI version. It
// is immutable and safe for concurrent use.
type Validator struct {
	versions map[string]*version
	limits   Limits
	mounted  *mounted // from Options.ValidatorsDir; nil without
}

// version is one QTI version's compiled XSDs and Schematron rules.
type version struct {
	name   string
	engine *xsd.Engine
	rules  *schematron.Engine
}

// ValidateOptions select the QTI version a document is validated against.
type ValidateOptions struct {
	// Version forces this QTI version, whatever the document declares. A
	// manifest whose schemaversion differs is then not reported as invalid
	// for that; a warning says the version was overridden.
	Version string
	// DefaultVersion is used when Version is empty and the document does
	// not declare a version; a package passes its manifest's version. Empty
	// selects the latest version.
	DefaultVersion string
}

// New compiles the schemas. It fails if any schema, or any include or import
// it needs, is missing or invalid, so problems surface at startup.
func New(opts Options) (*Validator, error) {
	fsys := opts.FS
	if fsys == nil {
		fsys = SchemaFS()
	}
	limits := opts.Limits
	if limits.MaxDocumentSize <= 0 {
		limits.MaxDocumentSize = DefaultLimits.MaxDocumentSize
	}
	if limits.MaxErrors <= 0 {
		limits.MaxErrors = DefaultLimits.MaxErrors
	}
	if limits.MaxDepth <= 0 {
		limits.MaxDepth = DefaultLimits.MaxDepth
	}

	var mu sync.Mutex
	seen := map[string]bool{}
	res := resolver{fsys: fsys, loaded: func(url string) {
		mu.Lock()
		defer mu.Unlock()
		if !seen[url] && opts.OnSchemaLoaded != nil {
			opts.OnSchemaLoaded(url)
		}
		seen[url] = true
	}}
	compiled, err := loadRules(fsys)
	if err != nil {
		return nil, err
	}
	v := &Validator{versions: map[string]*version{}, limits: limits}
	// The validators directory goes first: its mistakes are the likelier
	// ones, and they fail before the long compilation of the QTI schemas.
	if opts.ValidatorsDir != "" {
		if v.mounted, err = loadMounted(opts, res); err != nil {
			return nil, err
		}
	}
	for _, vs := range Versions {
		var sources []xsd.SchemaSource
		for _, u := range []string{vs.ASI, vs.Manifest} {
			src, err := res.source(u)
			if err != nil {
				return nil, err
			}
			sources = append(sources, src.WithResolver(res))
		}
		engine, err := xsd.Compile(sources...)
		if err != nil {
			return nil, fmt.Errorf("compile QTI %s schemas: %w", vs.Version, err)
		}
		subset, err := compiled.Subset(vs.ASI, lomSchemaURL, AdditionalRules)
		if err != nil {
			return nil, fmt.Errorf("QTI %s: %w", vs.Version, err)
		}
		rules, err := schematron.New(subset)
		if err != nil {
			return nil, fmt.Errorf("QTI %s: %w", vs.Version, err)
		}
		v.versions[vs.Version] = &version{name: vs.Version, engine: engine, rules: rules}
	}
	return v, nil
}

// loadRules loads the Schematron rules compiled at build time.
func loadRules(fsys fs.FS) (*schematron.Compiled, error) {
	f, err := fsys.Open(rulesFile)
	if err != nil {
		return nil, fmt.Errorf("Schematron rules: %w", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("Schematron rules: %w", err)
	}
	var compiled schematron.Compiled
	if err := json.NewDecoder(zr).Decode(&compiled); err != nil {
		return nil, fmt.Errorf("Schematron rules: %w", err)
	}
	return &compiled, nil
}

// Limits returns the limits in effect.
func (v *Validator) Limits() Limits { return v.limits }

// Validate reads one XML document from r and validates it: first against
// the XSDs of the selected QTI version, then against its Schematron rules,
// then against the mounted .sch files. A document type a mounted XSD adds
// is validated against that XSD instead, and has no QTI version.
// The document is held in memory, bounded by MaxDocumentSize.
func (v *Validator) Validate(ctx context.Context, r io.Reader, opts ValidateOptions) ValidationResult {
	lr := &limitReader{r: ctxReader{ctx: ctx, r: r}, remaining: v.limits.MaxDocumentSize}
	data, err := io.ReadAll(lr)
	switch {
	case lr.exceeded:
		return failure(OutcomeTooLarge, "", CodeTooLarge,
			fmt.Sprintf("document is larger than %d bytes", v.limits.MaxDocumentSize))
	case ctx.Err() != nil:
		return failure(OutcomeInternal, "", CodeInternal, "validation interrupted: "+ctx.Err().Error())
	case err != nil:
		return failure(OutcomeInternal, "", CodeInternal, "read: "+err.Error())
	}
	return v.validate(data, opts)
}

func (v *Validator) validate(data []byte, opts ValidateOptions) ValidationResult {
	name, _, err := detectRoot(bytes.NewReader(data))
	if err != nil {
		return detectionFailure(err)
	}
	doc, ok := lookupDocumentType(name)
	if !ok && v.mounted != nil {
		if ct, ok := v.mounted.types[name]; ok {
			return v.validateCustom(ct, data)
		}
	}
	if !ok {
		// Malformed XML is reported as such, whatever its root.
		if err := checkWellFormed(bytes.NewReader(data)); err != nil {
			return detectionFailure(err)
		}
		return failure(OutcomeInvalid, "", CodeUnsupportedDocument,
			fmt.Sprintf("root element {%s}%s is not a supported QTI 3 document", name.Space, name.Local))
	}
	ver, notice, mismatch := v.selectVersion(doc, data, opts)

	err = ver.engine.ValidateWithOptions(bytes.NewReader(data), xsd.ValidateOptions{
		MaxErrors:        v.limits.MaxErrors,
		MaxInstanceDepth: v.limits.MaxDepth,
		MaxInstanceBytes: v.limits.MaxDocumentSize,
	})
	res := ValidationResult{Valid: true, Schema: doc.Schema, Outcome: OutcomeValid}
	assessed := true
	if err != nil {
		res = fromLibraryError(doc.Schema, err)
		if mismatch {
			res = dropSchemaVersionErrors(res)
		}
		assessed = res.Outcome == OutcomeInvalid && xsderrorsConclusive(err) || res.Outcome == OutcomeValid
	}
	res.Version = ver.name
	if notice != nil {
		if notice.warning {
			res.Warnings = append(res.Warnings, notice.err)
		} else {
			res.Errors = append([]ValidationError{notice.err}, res.Errors...)
			res.Valid, res.Outcome = false, worse(res.Outcome, OutcomeInvalid)
		}
	}
	if !assessed {
		return res // not well-formed, too large, or not assessable
	}
	return v.checkRules(data, res, ruleEngine{Engine: ver.rules}, v.mountedRules())
}

// validateCustom validates a document whose root a mounted XSD declares:
// against that XSD, the rules embedded in it, and the mounted .sch files.
// QTI versions do not apply.
func (v *Validator) validateCustom(ct *customType, data []byte) ValidationResult {
	err := ct.engine.ValidateWithOptions(bytes.NewReader(data), xsd.ValidateOptions{
		MaxErrors:        v.limits.MaxErrors,
		MaxInstanceBytes: v.limits.MaxDocumentSize,
		MaxInstanceDepth: v.limits.MaxDepth,
	})
	res := ValidationResult{Valid: true, Schema: ct.Schema, Outcome: OutcomeValid}
	if err != nil {
		res = fromLibraryError(ct.Schema, err)
		for i := range res.Errors {
			if res.Errors[i].Code == CodeValidation {
				res.Errors[i].Source = ct.file
			}
		}
		if res.Outcome != OutcomeInvalid || !xsderrorsConclusive(err) {
			return res // not well-formed, too large, or not assessable
		}
	}
	return v.checkRules(data, res, ruleEngine{Engine: ct.rules, mounted: true}, v.mountedRules())
}

// ruleEngine is Schematron rules to run on a document. The findings of
// mounted rules name their file in Source.
type ruleEngine struct {
	*schematron.Engine
	mounted bool
}

// mountedRules returns the rules of the mounted .sch files; its Engine is
// nil without.
func (v *Validator) mountedRules() ruleEngine {
	if v.mounted == nil {
		return ruleEngine{}
	}
	return ruleEngine{Engine: v.mounted.rules, mounted: true}
}

// versionNotice reports how a manifest's declared version was handled.
type versionNotice struct {
	err     ValidationError
	warning bool
}

// selectVersion picks the QTI version for a document. A manifest declares
// its version in metadata/schemaversion; other documents do not. mismatch
// reports that the manifest's declared version differs from the one used,
// so the schema's error about it is replaced by the returned notice.
func (v *Validator) selectVersion(doc DocumentType, data []byte, opts ValidateOptions) (*version, *versionNotice, bool) {
	var declared schemaVersion
	if doc.Namespace == NamespaceManifest {
		declared = manifestSchemaVersion(data)
	}
	at := func(e ValidationError) ValidationError {
		e.Line, e.Column, e.Path = declared.line, declared.column, schemaVersionPath
		return e
	}
	switch {
	case opts.Version != "":
		ver := v.versions[opts.Version]
		if declared.value == "" || declared.value == ver.name {
			return ver, nil, false
		}
		return ver, &versionNotice{warning: true, err: at(ValidationError{
			Code: CodeVersionOverridden,
			Message: fmt.Sprintf("The manifest declares schemaversion %s; validated against QTI %s as requested.",
				declared.value, ver.name),
		})}, true
	case declared.value != "":
		if ver, ok := v.versions[declared.value]; ok {
			return ver, nil, false
		}
		ver := v.versions[LatestVersion()]
		return ver, &versionNotice{err: at(ValidationError{
			Code: CodeUnsupportedVersion,
			Message: fmt.Sprintf("The manifest declares schemaversion %q, which is not supported (supported: %s); validated against QTI %s.",
				declared.value, strings.Join(SupportedVersions(), ", "), ver.name),
		})}, true
	case opts.DefaultVersion != "" && v.versions[opts.DefaultVersion] != nil:
		return v.versions[opts.DefaultVersion], nil, false
	}
	return v.versions[LatestVersion()], nil, false
}

// dropSchemaVersionErrors removes the schema's verdict on the manifest's
// schemaversion, which the version notice replaces.
func dropSchemaVersionErrors(res ValidationResult) ValidationResult {
	kept := res.Errors[:0:0]
	for _, e := range res.Errors {
		if e.Code == CodeValidation && e.Path == schemaVersionPath {
			continue
		}
		kept = append(kept, e)
	}
	res.Errors = kept
	if len(kept) == 0 && res.Outcome == OutcomeInvalid {
		res.Valid, res.Outcome = true, OutcomeValid
	}
	return res
}

// checkRules runs Schematron rules on a well-formed document, engine by
// engine, and adds their findings to res. Nil engines are skipped.
func (v *Validator) checkRules(data []byte, res ValidationResult, engines ...ruleEngine) ValidationResult {
	tree, err := xpath.Parse(bytes.NewReader(data))
	if err != nil {
		return failure(OutcomeInternal, res.Schema, CodeInternal, "parse for Schematron: "+err.Error())
	}
	for _, rules := range engines {
		if rules.Engine == nil {
			continue
		}
		remaining := v.limits.MaxErrors - len(res.Errors)
		if remaining <= 0 {
			return res
		}
		failures, err := rules.Validate(tree, remaining)
		if err != nil {
			return failure(OutcomeInternal, res.Schema, CodeInternal, "Schematron: "+err.Error())
		}
		res = addRuleFailures(res, failures, rules.mounted)
	}
	return res
}

// addRuleFailures adds Schematron findings to res as errors or warnings.
func addRuleFailures(res ValidationResult, failures []schematron.Failure, mounted bool) ValidationResult {
	for _, f := range failures {
		e := ValidationError{
			Code:    CodeSchematron,
			Rule:    f.Pattern,
			Line:    f.Node.Line,
			Column:  f.Node.Column,
			Path:    f.Node.Path(),
			Message: f.Message,
		}
		if f.Assertion != "" {
			e.Rule += "/" + f.Assertion
		}
		switch {
		case mounted:
			e.Source = f.Source
		case f.Source == AdditionalRules:
			// Tells the validator's own rules apart from 1EdTech's in the
			// report's generator.
			e.Rule = AdditionalRules + "#" + e.Rule
		}
		if f.Warning {
			res.Warnings = append(res.Warnings, e)
			continue
		}
		res.Errors = append(res.Errors, e)
		res.Valid, res.Outcome = false, OutcomeInvalid
	}
	return res
}

func detectionFailure(err error) ValidationResult {
	switch {
	case errors.Is(err, errUnsupportedEncoding):
		return failure(OutcomeInvalid, "", CodeUnsupportedEncoding, err.Error())
	case errors.Is(err, errDTD):
		return failure(OutcomeInvalid, "", CodeUnsupportedXML, err.Error())
	}
	res := failure(OutcomeMalformed, "", CodeInvalidXML, err.Error())
	var syntax *xml.SyntaxError
	if errors.As(err, &syntax) {
		res.Errors[0].Line = syntax.Line
		res.Errors[0].Message = syntax.Msg
	}
	return res
}

var errTooLarge = errors.New("input exceeds the size limit")

// limitReader fails once more than remaining bytes are read, and remembers
// that it did, so callers can tell a size violation from malformed input.
type limitReader struct {
	r         io.Reader
	remaining int64
	exceeded  bool
}

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
