package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/kennisnet/qti3-validator/internal/domain/qti"
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

// Rules get the room MaxErrors leaves; none at all when the schema used it.
func TestRulesShareMaxErrors(t *testing.T) {
	rules := &fakeRules{}
	schema := &fakeSchema{verdict: SchemaVerdict{
		Outcome: qti.OutcomeInvalid, Conclusive: true,
		Findings: []qti.Finding{{Code: qti.CodeValidation}, {Code: qti.CodeValidation}},
	}}
	v := newTestValidator(t, schema, rules).WithLimits(qti.Limits{MaxErrors: 5})
	validateDoc(t, v, item)
	if rules.max != 3 {
		t.Fatalf("rules got max %d, want 3", rules.max)
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
