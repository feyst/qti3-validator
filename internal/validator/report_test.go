package validator

import (
	"strings"
	"testing"
	"time"
)

var testMeta = ReportMeta{Generator: "qti-validator test", InputName: "input", Now: time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)}

func TestDocumentReportValid(t *testing.T) {
	r := DocumentReport(validateString(t, readTestdata(t, "valid/assessment-item.xml"), ValidateOptions{}), testMeta)
	if r.Summary.Outcome != OutcomeNameValid || !r.IsValid() || r.Summary.TotalRun != 3 || r.Summary.Valid != 1 {
		t.Fatalf("summary %+v", r.Summary)
	}
	if r.Input.Type != "XML" || r.Specification.Version != "3.0.1" || r.Specification.PID != "qti301.pid" ||
		r.Generated != "2026-10-07T09:00:00" || len(r.ID) != 36 || strings.Count(r.ID, "-") != 4 {
		t.Fatalf("report %+v", r)
	}
	if len(r.Valids) != 1 || r.Valids[0].Location.Resource != "input" || !strings.Contains(r.Valids[0].Message, "qti-assessment-item") {
		t.Fatalf("valids %+v", r.Valids)
	}
	if r.Errors == nil || r.Fatals == nil || r.NotRun == nil {
		t.Fatal("empty lists must be present, not null")
	}
}

func TestDocumentReportErrors(t *testing.T) {
	r := DocumentReport(validateString(t, readTestdata(t, "invalid/schematron-unknown-attribute.xml"), ValidateOptions{}), testMeta)
	if r.Summary.Outcome != OutcomeNameError || r.IsValid() || r.Summary.Errors != 1 || r.Summary.Valid != 0 {
		t.Fatalf("summary %+v", r.Summary)
	}
	it := r.Errors[0]
	if it.Code != CodeSchematron || !strings.HasPrefix(it.Generator, "schematron|RULESET_") ||
		it.Location.Line != 18 || it.Location.Path == "" || it.DetailsMessage != nil {
		t.Fatalf("item %+v", it)
	}

	r = DocumentReport(validateString(t, readTestdata(t, "invalid/invalid-value.xml"), ValidateOptions{}), testMeta)
	if g := r.Errors[0].Generator; g != "xsd|"+Versions[1].ASI {
		t.Fatalf("generator %q", g)
	}
}

// A document that cannot be read is FATAL, and the checks after parsing are
// reported as not run.
func TestDocumentReportFatal(t *testing.T) {
	r := DocumentReport(validateString(t, readTestdata(t, "invalid/malformed.xml"), ValidateOptions{}), testMeta)
	if r.Summary.Outcome != OutcomeNameFatal || r.Summary.Fatals != 1 || r.Summary.NotRun != 1 || len(r.Valids) != 0 {
		t.Fatalf("summary %+v", r.Summary)
	}
	if r.Fatals[0].Code != CodeInvalidXML || r.Fatals[0].Generator != "parse" {
		t.Fatalf("fatal %+v", r.Fatals[0])
	}
}

// Warnings alone leave a report valid.
func TestReportWarningsStayValid(t *testing.T) {
	res := validateString(t, readTestdata(t, "package/imsmanifest.xml"), ValidateOptions{Version: "3.0.1"})
	r := DocumentReport(res, testMeta)
	if r.Summary.Outcome != OutcomeNameWarning || !r.IsValid() || r.Summary.Valid != 1 {
		t.Fatalf("summary %+v", r.Summary)
	}
	if r.Warnings[0].Code != CodeVersionOverridden || r.Warnings[0].Generator != "version" {
		t.Fatalf("warning %+v", r.Warnings[0])
	}
	if g := (DocumentReport(validateString(t, strings.Replace(readTestdata(t, "package/imsmanifest.xml"),
		`type="imsqti_item_xmlv3p0"`, `type="nope"`, 1), ValidateOptions{}), testMeta)).Errors[0].Generator; g != "xsd|"+Versions[0].Manifest {
		t.Fatalf("manifest generator %q", g)
	}
}

func TestPackageReport(t *testing.T) {
	entries := append(packageEntries(t),
		entry{"items/broken.xml", `<qti-assessment-item xmlns="` + NamespaceASI + `"><oops></qti-assessment-item>`},
		entry{"extra/config.xml", `<config/>`},
		entry{"../evil.xml", `<x/>`},
	)
	res := validatePackageWith(t, makeZip(t, entries...), ValidateOptions{})
	r := PackageReport(res, ReportMeta{Generator: "g", InputName: "toets.zip", Now: time.Now()})
	if r.Input.Type != "ZIP" || r.Input.Name != "toets.zip" || r.Specification.Version != "3.0.0" {
		t.Fatalf("report %+v", r)
	}
	// manifest, test, item-1 valid; broken.xml fatal; config.xml skipped.
	// Checks run: the package, three checks for each of the 3 valid documents,
	// and only reading for broken.xml and config.xml.
	if r.Summary.Outcome != OutcomeNameFatal || r.Summary.TotalRun != 12 || r.Summary.Valid != 3 || r.Summary.NotRun != 2 {
		t.Fatalf("summary %+v", r.Summary)
	}
	if r.Fatals[0].Location.Resource != "/items/broken.xml" || r.Fatals[0].Message == "" {
		t.Fatalf("fatal %+v", r.Fatals[0])
	}
	if r.Errors[0].Code != CodeUnsafePath || r.Errors[0].Location.Resource != "/../evil.xml" {
		t.Fatalf("package error %+v", r.Errors[0])
	}
}

func TestPackageReportUnreadableZip(t *testing.T) {
	res := validatePackageWith(t, []byte("not a zip"), ValidateOptions{})
	r := PackageReport(res, ReportMeta{InputName: "x.zip", Now: time.Now()})
	if r.Summary.Outcome != OutcomeNameFatal || r.Fatals[0].Code != CodeInvalidZIP || r.Fatals[0].Location.Resource != "x.zip" {
		t.Fatalf("report %+v", r)
	}
}

func TestMountedSourceInGenerator(t *testing.T) {
	it := itemFor(ValidationError{Code: CodeSchematron, Rule: "house-title", Source: "house-rules.sch", Message: "m"}, "a.xml", "qti-assessment-item", "3.0.1")
	if it.Generator != "schematron|house-rules.sch#house-title" || it.Source != "house-rules.sch" || it.Outcome != OutcomeNameError {
		t.Fatalf("item %+v", it)
	}
	it = itemFor(ValidationError{Code: CodeValidation, Source: "mine.xsd"}, "a.xml", "mine", "")
	if it.Generator != "xsd|mine.xsd" {
		t.Fatalf("item %+v", it)
	}
}
