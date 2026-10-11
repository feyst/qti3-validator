package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"qti3-validator/internal/domain/qti"
)

func TestNewRequiresEveryVersion(t *testing.T) {
	_, err := New(Config{Versions: map[string]Profile{"3.0.0": {Schema: &fakeSchema{}}}, OpenArchive: nil})
	if err == nil || !strings.Contains(err.Error(), "3.0.1") {
		t.Fatalf("got %v", err)
	}
}

func TestNewRejectsCustomTypeClashes(t *testing.T) {
	versions := map[string]Profile{}
	for _, n := range qti.SupportedVersions() {
		versions[n] = Profile{Schema: &fakeSchema{}}
	}
	item, _ := qti.LookupDocumentType(qti.DocumentTypes()[0].Name())
	custom := qti.DocumentType{Namespace: "urn:x", Root: "x", Schema: "x"}
	for name, types := range map[string][]CustomType{
		"QTI type": {{Type: item}},
		"twice":    {{Type: custom}, {Type: custom}},
	} {
		if _, err := New(Config{Versions: versions, CustomTypes: types, OpenArchive: newTestValidator(t, &fakeSchema{}, nil).openArchive}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// Rules run after a conclusive schema check, and their warnings leave the
// document valid.
func TestRulesRunAfterSchema(t *testing.T) {
	rules := &fakeRules{findings: []RuleFinding{
		{Finding: qti.Finding{Code: qti.CodeSchematron, Message: "warn"}, Warning: true},
	}}
	res := validateDoc(t, newTestValidator(t, &fakeSchema{verdict: validVerdict}, rules), item)
	if !res.Valid || res.Outcome != qti.OutcomeValid || len(res.Warnings) != 1 || res.Version != qti.LatestVersion().Name {
		t.Fatalf("got %+v", res)
	}
	rules.findings = append(rules.findings, RuleFinding{Finding: qti.Finding{Code: qti.CodeSchematron, Message: "err"}})
	res = validateDoc(t, newTestValidator(t, &fakeSchema{verdict: validVerdict}, rules), item)
	if res.Valid || res.Outcome != qti.OutcomeInvalid || len(res.Errors) != 1 {
		t.Fatalf("got %+v", res)
	}
}

func TestRulesSkippedWhenSchemaInconclusive(t *testing.T) {
	for name, verdict := range map[string]SchemaVerdict{
		"inconclusive": {Outcome: qti.OutcomeInvalid, Findings: []qti.Finding{{Code: qti.CodeValidation}}},
		"malformed":    {Outcome: qti.OutcomeMalformed, Conclusive: true, Findings: []qti.Finding{{Code: qti.CodeInvalidXML}}},
	} {
		rules := &fakeRules{}
		validateDoc(t, newTestValidator(t, &fakeSchema{verdict: verdict}, rules), item)
		if rules.checked != 0 {
			t.Errorf("%s: rules ran", name)
		}
	}
}

// Rules get the room for errors MaxErrors leaves; none at all when the
// schema used it. Their warnings have a limit of their own, MaxErrors.
func TestRulesShareMaxErrors(t *testing.T) {
	rules := &fakeRules{}
	schema := &fakeSchema{verdict: SchemaVerdict{
		Outcome: qti.OutcomeInvalid, Conclusive: true,
		Findings: []qti.Finding{{Code: qti.CodeValidation}, {Code: qti.CodeValidation}},
	}}
	v := newTestValidator(t, schema, rules).WithLimits(qti.Limits{MaxErrors: 5})
	validateDoc(t, v, item)
	if want := (RuleLimits{Errors: 3, Warnings: 5}); rules.limits != want {
		t.Fatalf("rules got limits %+v, want %+v", rules.limits, want)
	}
	rules.checked = 0
	validateDoc(t, v.WithLimits(qti.Limits{MaxErrors: 2}), item)
	if rules.checked != 0 {
		t.Fatal("rules ran with no room left")
	}
}

func TestRuleFailureIsInternal(t *testing.T) {
	rules := &fakeRules{err: errors.New("Schematron: boom")}
	res := validateDoc(t, newTestValidator(t, &fakeSchema{verdict: validVerdict}, rules), item)
	if res.Outcome != qti.OutcomeInternal || res.Errors[0].Code != qti.CodeInternal || res.Errors[0].Message != "Schematron: boom" {
		t.Fatalf("got %+v", res)
	}
}

// A rule failure keeps what was found before it: the schema errors, the
// version notice and the version.
func TestRuleFailureKeepsEarlierFindings(t *testing.T) {
	rules := &fakeRules{err: errors.New("Schematron: boom")}
	schema := &fakeSchema{verdict: SchemaVerdict{
		Outcome: qti.OutcomeInvalid, Conclusive: true, Findings: []qti.Finding{{Code: qti.CodeValidation, Message: "bad"}},
	}}
	v := newTestValidator(t, schema, rules)
	res := v.ValidateDocument(t.Context(), ValidateDocument{Document: strings.NewReader(manifest("3.0.0")), Version: "3.0.1"})
	if res.Outcome != qti.OutcomeInternal || res.Valid || res.Version != "3.0.1" || res.Schema != "imscp-manifest" ||
		len(res.Errors) != 2 || res.Errors[0].Code != qti.CodeValidation || res.Errors[1].Code != qti.CodeInternal ||
		len(res.Warnings) != 1 || res.Warnings[0].Code != qti.CodeVersionOverridden {
		t.Fatalf("got %+v", res)
	}
}

// A rule failure also keeps the findings of the rules before it, such as
// those of the built-in rules before a mounted rule set that fails.
func TestRuleFailureKeepsEarlierRuleFindings(t *testing.T) {
	rules := &fakeRules{
		findings: []RuleFinding{
			{Finding: qti.Finding{Code: qti.CodeSchematron, Message: "error"}},
			{Finding: qti.Finding{Code: qti.CodeSchematron, Message: "warning"}, Warning: true},
		},
		err: errors.New("Schematron: boom"),
	}
	res := validateDoc(t, newTestValidator(t, &fakeSchema{verdict: validVerdict}, rules), item)
	if res.Outcome != qti.OutcomeInternal || res.Valid || len(res.Errors) != 2 ||
		res.Errors[0].Message != "error" || res.Errors[1].Code != qti.CodeInternal ||
		len(res.Warnings) != 1 || res.Warnings[0].Message != "warning" {
		t.Fatalf("got %+v", res)
	}
}

func TestDocumentDetection(t *testing.T) {
	v := newTestValidator(t, &fakeSchema{verdict: validVerdict}, nil)
	for doc, code := range map[string]qti.Code{
		`<x/>`:             qti.CodeUnsupportedDocument,
		`<x>`:              qti.CodeInvalidXML,
		`<!DOCTYPE x><x/>`: qti.CodeUnsupportedXML,
		`<?xml version="1.0" encoding="UTF-16"?><x/>`: qti.CodeUnsupportedEncoding,
	} {
		if res := validateDoc(t, v, doc); res.Valid || res.Errors[0].Code != code {
			t.Errorf("%q: got %+v, want %s", doc, res, code)
		}
	}
	if res := validateDoc(t, v.WithLimits(qti.Limits{MaxDocumentSize: 10}), item); res.Outcome != qti.OutcomeTooLarge {
		t.Errorf("too large: got %+v", res)
	}
}

// A document that could not be read far enough to know its type gets the
// version it would have been validated against: the forced one, or the
// latest.
func TestUnreadableDocumentGetsVersion(t *testing.T) {
	v := newTestValidator(t, &fakeSchema{verdict: validVerdict}, nil)
	for _, doc := range []string{`<x/>`, `<x>`, `<!DOCTYPE x><x/>`} {
		if res := validateDoc(t, v, doc); res.Version != qti.LatestVersion().Name {
			t.Errorf("%q: version %q, want %q", doc, res.Version, qti.LatestVersion().Name)
		}
		res := v.ValidateDocument(t.Context(), ValidateDocument{Document: strings.NewReader(doc), Version: "3.0.0"})
		if res.Version != "3.0.0" {
			t.Errorf("%q forced to 3.0.0: version %q", doc, res.Version)
		}
	}
	if res := validateDoc(t, v.WithLimits(qti.Limits{MaxDocumentSize: 10}), item); res.Version != qti.LatestVersion().Name {
		t.Errorf("too large: version %q", res.Version)
	}
}

// Ran records the checks that ran, for the report's totalRun.
func TestRanRecordsChecks(t *testing.T) {
	invalid := SchemaVerdict{Outcome: qti.OutcomeInvalid, Conclusive: true, Findings: []qti.Finding{{Code: qti.CodeValidation}}}
	inconclusive := SchemaVerdict{Outcome: qti.OutcomeInvalid, Findings: []qti.Finding{{Code: qti.CodeValidation}}}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		verdict SchemaVerdict
		limits  qti.Limits
		doc     string
		want    qti.Checks
	}{
		{"valid", t.Context(), validVerdict, qti.Limits{}, item, qti.Checks{Schema: true, Rules: true}},
		{"invalid", t.Context(), invalid, qti.Limits{}, item, qti.Checks{Schema: true, Rules: true}},
		{"inconclusive", t.Context(), inconclusive, qti.Limits{}, item, qti.Checks{Schema: true}},
		{"MAX_ERRORS reached", t.Context(), invalid, qti.Limits{MaxErrors: 1}, item, qti.Checks{Schema: true}},
		{"stopped before", cancelled, validVerdict, qti.Limits{}, item, qti.Checks{}},
		{"malformed", t.Context(), validVerdict, qti.Limits{}, `<x>`, qti.Checks{}},
	} {
		v := newTestValidator(t, &fakeSchema{verdict: tc.verdict}, &fakeRules{}).WithLimits(tc.limits)
		res := v.ValidateDocument(tc.ctx, ValidateDocument{Document: strings.NewReader(tc.doc)})
		if res.Ran != tc.want {
			t.Errorf("%s: ran %+v, want %+v", tc.name, res.Ran, tc.want)
		}
	}
}

// A manifest's declared version selects the version, and a forced version
// replaces the schema's verdict on it with a warning.
func TestManifestVersion(t *testing.T) {
	schema := &fakeSchema{verdict: SchemaVerdict{
		Outcome: qti.OutcomeInvalid, Conclusive: true,
		Findings: []qti.Finding{{Code: qti.CodeValidation, Path: qti.SchemaVersionPath}},
	}}
	rules := &fakeRules{}
	v := newTestValidator(t, schema, rules)
	res := v.ValidateDocument(t.Context(), ValidateDocument{Document: strings.NewReader(manifest("3.0.0")), Version: "3.0.1"})
	if !res.Valid || res.Version != "3.0.1" || len(res.Warnings) != 1 || res.Warnings[0].Code != qti.CodeVersionOverridden {
		t.Fatalf("got %+v", res)
	}
	if rules.checked != 1 {
		t.Fatal("rules did not run once the version verdict was dropped")
	}
	res = validateDoc(t, newTestValidator(t, &fakeSchema{verdict: validVerdict}, nil), manifest("3.0.0"))
	if res.Version != "3.0.0" {
		t.Fatalf("declared version not used: %+v", res)
	}
	res = validateDoc(t, newTestValidator(t, &fakeSchema{verdict: validVerdict}, nil), manifest("2.2"))
	if res.Valid || res.Version != qti.LatestVersion().Name || res.Errors[0].Code != qti.CodeUnsupportedVersion {
		t.Fatalf("unsupported declared version: %+v", res)
	}
}

func TestPackage(t *testing.T) {
	v := newTestValidator(t, &fakeSchema{verdict: validVerdict}, nil,
		fakeEntry{name: qti.ManifestName, data: manifest("3.0.0")},
		fakeEntry{name: "item.xml", data: item},
		fakeEntry{name: "media/", data: ""},
		fakeEntry{name: "media/a.png", data: "png"},
		fakeEntry{name: "stray.xml", data: `<x/>`},
	)
	res := validatePkg(t, v, qti.PackageLimits{})
	if !res.Valid || res.Version != "3.0.0" || len(res.Files) != 3 {
		t.Fatalf("got %+v", res)
	}
	if f := res.Files[2]; f.File != "stray.xml" || !f.Skipped {
		t.Fatalf("stray.xml: %+v", f)
	}
}

func TestPackageFailures(t *testing.T) {
	m := fakeEntry{name: qti.ManifestName, data: manifest("3.0.0")}
	tests := []struct {
		name    string
		entries []ArchiveEntry
		limits  qti.PackageLimits
		outcome qti.Outcome
		code    qti.Code
	}{
		{"not a zip", nil, qti.PackageLimits{}, qti.OutcomeMalformed, qti.CodeInvalidZIP},
		{"too many files", []ArchiveEntry{m, m}, qti.PackageLimits{MaxFiles: 1}, qti.OutcomeTooLarge, qti.CodeTooManyFiles},
		{"unsafe path", []ArchiveEntry{m, fakeEntry{name: "../x.xml", data: item}}, qti.PackageLimits{}, qti.OutcomeInvalid, qti.CodeUnsafePath},
		{"no manifest", []ArchiveEntry{fakeEntry{name: "item.xml", data: item}}, qti.PackageLimits{}, qti.OutcomeInvalid, qti.CodeMissingManifest},
		{"budget", []ArchiveEntry{m, fakeEntry{name: "item.xml", data: item}}, qti.PackageLimits{MaxUncompressedSize: int64(len(m.data)) + 5}, qti.OutcomeTooLarge, qti.CodeTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := validatePkg(t, newTestValidator(t, &fakeSchema{verdict: validVerdict}, nil, tt.entries...), tt.limits)
			if res.Valid || res.Outcome != tt.outcome || res.Errors[len(res.Errors)-1].Code != tt.code {
				t.Fatalf("got %+v", res)
			}
		})
	}
}

func TestPackageEntryFailures(t *testing.T) {
	m := fakeEntry{name: qti.ManifestName, data: manifest("3.0.0")}
	tests := []struct {
		name  string
		entry fakeEntry
		code  qti.Code
	}{
		{"declared too large", fakeEntry{name: "item.xml", data: item, declared: 1 << 40}, qti.CodeTooLarge},
		{"lying header", fakeEntry{name: "item.xml", data: item, readErr: errors.New("zip: checksum error")}, qti.CodeInvalidZIP},
		{"QTI resource of another type", fakeEntry{name: "item.xml", data: `<x/>`}, qti.CodeUnsupportedDocument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := validatePkg(t, newTestValidator(t, &fakeSchema{verdict: validVerdict}, nil, m, tt.entry), qti.PackageLimits{})
			f := res.Files[len(res.Files)-1]
			if res.Valid || f.Valid || f.Errors[0].Code != tt.code || f.Errors[0].File != "item.xml" {
				t.Fatalf("got %+v", res)
			}
		})
	}
}

// Rules that stop before the end keep the findings so far, the schema's and
// their own, and add too_complex, whether the budget or the time ran out.
func TestRulesStopped(t *testing.T) {
	schema := &fakeSchema{verdict: SchemaVerdict{
		Outcome: qti.OutcomeInvalid, Conclusive: true, Findings: []qti.Finding{{Code: qti.CodeValidation}},
	}}
	for name, err := range map[string]error{
		"budget":   fmt.Errorf("%w: out of steps", ErrTooComplex),
		"deadline": fmt.Errorf("xpath: %w", context.DeadlineExceeded),
	} {
		rules := &fakeRules{findings: []RuleFinding{{Finding: qti.Finding{Code: qti.CodeSchematron}}}, err: err}
		res := validateDoc(t, newTestValidator(t, schema, rules), item)
		if res.Valid || res.Outcome != qti.OutcomeTooComplex || len(res.Errors) != 3 ||
			res.Errors[0].Code != qti.CodeValidation || res.Errors[1].Code != qti.CodeSchematron ||
			res.Errors[2].Code != qti.CodeTooComplex || !strings.HasPrefix(res.Errors[2].Message, "validation stopped: ") {
			t.Errorf("%s: got %+v", name, res)
		}
	}
}

// The schema check cannot be interrupted, but a context that ends during it
// stops the validation before the rules.
func TestContextEndsDuringSchemaCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rules := &fakeRules{}
	v := newTestValidator(t, &fakeSchema{verdict: validVerdict, during: cancel}, rules)
	res := v.ValidateDocument(ctx, ValidateDocument{Document: strings.NewReader(item)})
	if res.Valid || res.Outcome != qti.OutcomeTooComplex || len(res.Errors) != 1 || res.Errors[0].Code != qti.CodeTooComplex {
		t.Fatalf("got %+v", res)
	}
	if rules.checked != 0 {
		t.Fatal("rules ran after the context ended")
	}
}

// A document that is too complex is reported in its file; the other files
// are still validated.
func TestPackageDocumentTooComplex(t *testing.T) {
	rules := &fakeRules{err: ErrTooComplex}
	v := newTestValidator(t, &fakeSchema{verdict: validVerdict}, rules,
		fakeEntry{name: qti.ManifestName, data: manifest("3.0.0")},
		fakeEntry{name: "item.xml", data: item},
	)
	res := validatePkg(t, v, qti.PackageLimits{})
	if res.Valid || res.Outcome != qti.OutcomeTooComplex || len(res.Errors) != 0 || len(res.Files) != 2 {
		t.Fatalf("got %+v", res)
	}
	f := fileResult(t, res, "item.xml")
	if f.Valid || len(f.Errors) != 1 || f.Errors[0].Code != qti.CodeTooComplex || f.Errors[0].File != "item.xml" {
		t.Fatalf("item.xml: %+v", f)
	}
}

// Out of time, a package stops: the file that was running reports
// too_complex, and the package names the files that were not validated.
func TestPackageOutOfTime(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	v := newTestValidator(t, &fakeSchema{verdict: validVerdict, during: cancel}, nil,
		fakeEntry{name: qti.ManifestName, data: manifest("3.0.0")},
		fakeEntry{name: "item.xml", data: item},
	)
	res := v.ValidatePackage(ctx, ValidatePackage{Package: bytes.NewReader(nil)})
	if res.Valid || res.Outcome != qti.OutcomeTooComplex || len(res.Files) != 1 || len(res.Errors) != 1 {
		t.Fatalf("got %+v", res)
	}
	if f := res.Files[0]; f.File != qti.ManifestName || f.Errors[0].Code != qti.CodeTooComplex {
		t.Fatalf("manifest: %+v", f)
	}
	if e := res.Errors[0]; e.Code != qti.CodeTooComplex || !strings.Contains(e.Message, "item.xml and the files after it were not validated") {
		t.Fatalf("package: %+v", e)
	}
}

// Out of time while a file is read, a package stops the same way, not with
// an internal error.
func TestPackageOutOfTimeWhileReading(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	v := newTestValidator(t, &fakeSchema{verdict: validVerdict}, nil,
		fakeEntry{name: qti.ManifestName, data: manifest("3.0.0")},
		fakeEntry{name: "item.xml", data: item, during: cancel},
		fakeEntry{name: "item-2.xml", data: item},
	)
	res := v.ValidatePackage(ctx, ValidatePackage{Package: bytes.NewReader(nil)})
	if res.Valid || res.Outcome != qti.OutcomeTooComplex || len(res.Files) != 2 || len(res.Errors) != 1 {
		t.Fatalf("got %+v", res)
	}
	f := fileResult(t, res, "item.xml")
	if f.Valid || len(f.Errors) != 1 || f.Errors[0].Code != qti.CodeTooComplex || f.Errors[0].File != "item.xml" ||
		!strings.Contains(f.Errors[0].Message, "cancelled") {
		t.Fatalf("item.xml: %+v", f)
	}
	if e := res.Errors[0]; !strings.Contains(e.Message, "item-2.xml and the files after it were not validated") {
		t.Fatalf("package: %+v", e)
	}
}

// Out of time while the included files are validated as fragments, a
// package stops the same way: the fragment that was running reports
// too_complex, and the package names the ones that were not validated.
func TestPackageOutOfTimeInFragments(t *testing.T) {
	const div = `<div xmlns="` + qti.NamespaceASI + `"/>`
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	schema := &fakeSchema{verdict: validVerdict}
	// The manifest and the item are checked first; the fragments only once
	// the item is known to include them.
	schema.during = func() {
		if schema.checked == 3 {
			cancel()
		}
	}
	cfg := Config{
		Versions:   map[string]Profile{},
		References: fakeReferences{"frag-a.xml", "frag-b.xml", "frag-c.xml"},
		OpenArchive: func(io.ReaderAt, int64, int) (Archive, error) {
			return fakeArchive{
				fakeEntry{name: qti.ManifestName, data: manifest("3.0.0")},
				fakeEntry{name: "frag-a.xml", data: div},
				fakeEntry{name: "item.xml", data: item},
				fakeEntry{name: "frag-b.xml", data: div},
				fakeEntry{name: "frag-c.xml", data: div},
			}, nil
		},
	}
	for _, name := range qti.SupportedVersions() {
		cfg.Versions[name] = Profile{Schema: schema}
	}
	v, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	res := v.ValidatePackage(ctx, ValidatePackage{Package: bytes.NewReader(nil)})
	if res.Valid || res.Outcome != qti.OutcomeTooComplex || len(res.Files) != 5 || schema.checked != 3 {
		t.Fatalf("got %+v after %d schema checks", res, schema.checked)
	}
	for _, name := range []string{"frag-a.xml", "frag-b.xml", "frag-c.xml"} {
		if f := fileResult(t, res, name); f.Valid || f.Skipped || len(f.Errors) != 1 || f.Errors[0].Code != qti.CodeTooComplex {
			t.Errorf("%s: %+v", name, f)
		}
	}
	var stops []string
	for _, e := range res.Errors {
		if e.Code == qti.CodeTooComplex {
			stops = append(stops, e.Message)
		}
	}
	if len(stops) != 1 || !strings.Contains(stops[0], "cancelled; frag-b.xml and the included files after it were not validated as QTI fragments") {
		t.Fatalf("package: %+v", res.Errors)
	}
}

// A single document whose reading runs out of time is too complex, not an
// internal error.
func TestOutOfTimeWhileReading(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	v := newTestValidator(t, &fakeSchema{verdict: validVerdict}, nil)
	res := v.ValidateDocument(ctx, ValidateDocument{Document: strings.NewReader(item)})
	if res.Valid || res.Outcome != qti.OutcomeTooComplex || len(res.Errors) != 1 || res.Errors[0].Code != qti.CodeTooComplex ||
		!strings.Contains(res.Errors[0].Message, "REQUEST_TIMEOUT") {
		t.Fatalf("got %+v", res)
	}
}

func fileResult(t *testing.T, res qti.PackageResult, name string) qti.FileResult {
	t.Helper()
	for _, f := range res.Files {
		if f.File == name {
			return f
		}
	}
	t.Fatalf("no result for %s in %+v", name, res.Files)
	return qti.FileResult{}
}
