package report

import (
	"strings"
	"testing"
	"time"

	"qti3-validator/internal/domain/qti"
)

var testMeta = Meta{Generator: "qti-validator test", InputName: "input", Now: time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)}

var ranBoth = qti.Checks{Schema: true, Rules: true}

func validItem(version string) qti.DocumentResult {
	return qti.DocumentResult{Valid: true, Schema: "qti-assessment-item", Version: version, Outcome: qti.OutcomeValid, Ran: ranBoth}
}

func TestDocumentReportValid(t *testing.T) {
	r := ForDocument(validItem("3.0.1"), testMeta)
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
	res := qti.DocumentResult{Schema: "qti-assessment-item", Version: "3.0.1", Outcome: qti.OutcomeInvalid, Errors: []qti.Finding{
		{Code: qti.CodeSchematron, Rule: "RULESET_X/A1", Line: 18, Column: 5, Path: "/qti-assessment-item[1]", Message: "m"},
		{Code: qti.CodeValidation, Message: "bad value"},
	}}
	r := ForDocument(res, testMeta)
	if r.Summary.Outcome != OutcomeNameError || r.IsValid() || r.Summary.Errors != 2 || r.Summary.Valid != 0 {
		t.Fatalf("summary %+v", r.Summary)
	}
	it := r.Errors[0]
	if it.Code != string(qti.CodeSchematron) || it.Generator != "schematron|RULESET_X/A1" ||
		it.Location.Line != 18 || it.Location.Column != 5 || it.Location.Path == "" || it.DetailsMessage != nil {
		t.Fatalf("item %+v", it)
	}
	v301, _ := qti.LookupVersion("3.0.1")
	if g := r.Errors[1].Generator; g != "xsd|"+v301.ASI {
		t.Fatalf("generator %q", g)
	}
}

// A document that cannot be read is FATAL, and the checks after parsing are
// reported as not run.
func TestDocumentReportFatal(t *testing.T) {
	res := qti.Failure(qti.OutcomeMalformed, "qti-assessment-item", qti.CodeInvalidXML, "unexpected EOF")
	r := ForDocument(res, testMeta)
	if r.Summary.Outcome != OutcomeNameFatal || r.Summary.Fatals != 1 || r.Summary.NotRun != 1 || len(r.Valids) != 0 {
		t.Fatalf("summary %+v", r.Summary)
	}
	if r.Fatals[0].Code != string(qti.CodeInvalidXML) || r.Fatals[0].Generator != "parse" {
		t.Fatalf("fatal %+v", r.Fatals[0])
	}
}

// A validation that stopped is an EXCEPTION, after the errors found before.
func TestDocumentReportTooComplex(t *testing.T) {
	res := qti.DocumentResult{Schema: "qti-assessment-item", Version: "3.0.1", Outcome: qti.OutcomeTooComplex, Ran: ranBoth, Errors: []qti.Finding{
		{Code: qti.CodeValidation, Message: "bad value"},
		{Code: qti.CodeTooComplex, Message: "validation stopped: the document is too complex to validate"},
	}}
	r := ForDocument(res, testMeta)
	if r.Summary.Outcome != OutcomeNameException || r.IsValid() || r.Summary.Errors != 1 || r.Summary.Exceptions != 1 ||
		r.Summary.TotalRun != 3 || r.Summary.Valid != 0 || len(r.Valids) != 0 {
		t.Fatalf("summary %+v", r.Summary)
	}
	if it := r.Exceptions[0]; it.Code != string(qti.CodeTooComplex) || it.Generator != "limits" || it.Title != "Limits" {
		t.Fatalf("exception %+v", it)
	}
}

// Warnings alone leave a report valid. A manifest's schema is the manifest
// schema of its version.
func TestReportWarningsStayValid(t *testing.T) {
	res := qti.DocumentResult{
		Valid: true, Schema: "imscp-manifest", Version: "3.0.1", Outcome: qti.OutcomeValid,
		Warnings: []qti.Finding{{Code: qti.CodeVersionOverridden, Message: "overridden"}},
	}
	r := ForDocument(res, testMeta)
	if r.Summary.Outcome != OutcomeNameWarning || !r.IsValid() || r.Summary.Valid != 1 {
		t.Fatalf("summary %+v", r.Summary)
	}
	if r.Warnings[0].Code != string(qti.CodeVersionOverridden) || r.Warnings[0].Generator != "version" {
		t.Fatalf("warning %+v", r.Warnings[0])
	}
	// The note that rule warnings were left out is a warning too, though its
	// code is an error elsewhere.
	res.Warnings = []qti.Finding{{Code: qti.CodeLimitExceeded, Message: "3 further warnings"}}
	r = ForDocument(res, testMeta)
	if r.Summary.Outcome != OutcomeNameWarning || !r.IsValid() || len(r.Errors) != 0 ||
		r.Warnings[0].Outcome != OutcomeNameWarning || r.Warnings[0].Title != "Limits" || r.Warnings[0].Generator != "limits" {
		t.Fatalf("summary %+v, warnings %+v", r.Summary, r.Warnings)
	}
	res = qti.DocumentResult{
		Schema: "imscp-manifest", Version: "3.0.0", Outcome: qti.OutcomeInvalid,
		Errors: []qti.Finding{{Code: qti.CodeValidation, Message: "bad type"}},
	}
	v300, _ := qti.LookupVersion("3.0.0")
	if g := ForDocument(res, testMeta).Errors[0].Generator; g != "xsd|"+v300.Manifest {
		t.Fatalf("manifest generator %q", g)
	}
}

func TestPackageReport(t *testing.T) {
	broken := qti.Failure(qti.OutcomeMalformed, "", qti.CodeInvalidXML, "unexpected EOF")
	res := qti.PackageResult{
		Version: "3.0.0", Outcome: qti.OutcomeMalformed,
		Errors: []qti.Finding{{Code: qti.CodeUnsafePath, File: "../evil.xml", Message: "unsafe"}},
		Files: []qti.FileResult{
			{File: "imsmanifest.xml", Valid: true, Schema: "imscp-manifest", Version: "3.0.0", Ran: ranBoth},
			{File: "test.xml", Valid: true, Schema: "qti-assessment-test", Version: "3.0.0", Ran: ranBoth},
			{File: "items/item-1.xml", Valid: true, Schema: "qti-assessment-item", Version: "3.0.0", Ran: ranBoth},
			{File: "items/broken.xml", Errors: broken.Errors},
			{File: "extra/config.xml", Valid: true, Skipped: true},
		},
	}
	r := ForPackage(res, Meta{Generator: "g", InputName: "toets.zip", Now: time.Now()})
	if r.Input.Type != "ZIP" || r.Input.Name != "toets.zip" || r.Specification.Version != "3.0.0" {
		t.Fatalf("report %+v", r)
	}
	// Checks run: the package, three checks for each of the 3 valid
	// documents, and only reading for broken.xml and config.xml.
	if r.Summary.Outcome != OutcomeNameFatal || r.Summary.TotalRun != 12 || r.Summary.Valid != 3 || r.Summary.NotRun != 2 {
		t.Fatalf("summary %+v", r.Summary)
	}
	if r.Fatals[0].Location.Resource != "/items/broken.xml" || r.Fatals[0].Message == "" {
		t.Fatalf("fatal %+v", r.Fatals[0])
	}
	if r.Errors[0].Code != string(qti.CodeUnsafePath) || r.Errors[0].Location.Resource != "/../evil.xml" {
		t.Fatalf("package error %+v", r.Errors[0])
	}
}

// totalRun counts the checks that ran: reading, and the XML Schema and the
// Schematron rules only when they ran.
func TestTotalRunCountsChecksThatRan(t *testing.T) {
	for _, tc := range []struct {
		ran  qti.Checks
		want int
	}{
		{ranBoth, 3},
		{qti.Checks{Schema: true}, 2}, // the rules did not run: MAX_ERRORS reached, or the schema check was not conclusive
		{qti.Checks{}, 1},             // stopped before the schema check, or too large
	} {
		res := qti.DocumentResult{
			Schema: "qti-assessment-item", Version: "3.0.1", Outcome: qti.OutcomeInvalid, Ran: tc.ran,
			Errors: []qti.Finding{{Code: qti.CodeValidation, Message: "bad"}},
		}
		if got := ForDocument(res, testMeta).Summary.TotalRun; got != tc.want {
			t.Errorf("%+v: totalRun %d, want %d", tc.ran, got, tc.want)
		}
		pkg := qti.PackageResult{Version: "3.0.1", Outcome: qti.OutcomeInvalid, Files: []qti.FileResult{
			{File: "item.xml", Schema: res.Schema, Version: res.Version, Errors: res.Errors, Ran: tc.ran},
		}}
		if got := ForPackage(pkg, testMeta).Summary.TotalRun; got != tc.want+1 {
			t.Errorf("package %+v: totalRun %d, want %d", tc.ran, got, tc.want+1)
		}
	}
}

func TestPackageReportUnreadableZip(t *testing.T) {
	res := qti.PackageResult{Outcome: qti.OutcomeMalformed, Errors: []qti.Finding{{Code: qti.CodeInvalidZIP, Message: "not a zip"}}}
	r := ForPackage(res, Meta{InputName: "x.zip", Now: time.Now()})
	if r.Summary.Outcome != OutcomeNameFatal || r.Fatals[0].Code != string(qti.CodeInvalidZIP) || r.Fatals[0].Location.Resource != "x.zip" {
		t.Fatalf("report %+v", r)
	}
}

func TestMountedSourceInGenerator(t *testing.T) {
	it := itemFor(qti.Finding{Code: qti.CodeSchematron, Rule: "house-title", Source: "house-rules.sch", Message: "m"}, "a.xml", "qti-assessment-item", "3.0.1")
	if it.Generator != "schematron|house-rules.sch#house-title" || it.Source != "house-rules.sch" || it.Outcome != OutcomeNameError {
		t.Fatalf("item %+v", it)
	}
	it = itemFor(qti.Finding{Code: qti.CodeValidation, Source: "mine.xsd"}, "a.xml", "mine", "")
	if it.Generator != "xsd|mine.xsd" {
		t.Fatalf("item %+v", it)
	}
}
