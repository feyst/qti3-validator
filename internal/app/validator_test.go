package app_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"qti3-validator/internal/app"
	"qti3-validator/internal/domain/qti"
)

func TestValidAssessmentItem(t *testing.T) {
	res := validateFile(t, testValidator(t), "valid/assessment-item.xml")
	if !res.Valid || res.Outcome != qti.OutcomeValid || res.Schema != "qti-assessment-item" || len(res.Errors) != 0 {
		t.Fatalf("got %+v", res)
	}
}

func TestInvalidDocuments(t *testing.T) {
	tests := []struct {
		file    string
		outcome qti.Outcome
		code    qti.Code
		schema  string
		message string
	}{
		{"invalid/malformed.xml", qti.OutcomeMalformed, qti.CodeInvalidXML, "qti-assessment-item", ""},
		{"invalid/missing-required-element.xml", qti.OutcomeInvalid, qti.CodeValidation, "qti-assessment-item", ""},
		{"invalid/invalid-attribute.xml", qti.OutcomeInvalid, qti.CodeValidation, "qti-assessment-item", "response-identifier"},
		{"invalid/invalid-value.xml", qti.OutcomeInvalid, qti.CodeValidation, "qti-assessment-item", "shuffle"},
		{"invalid/invalid-cardinality.xml", qti.OutcomeInvalid, qti.CodeValidation, "qti-assessment-item", ""},
		{"invalid/wrong-namespace.xml", qti.OutcomeInvalid, qti.CodeUnsupportedDocument, "", "imsqti_v2p2"},
		{"invalid/unknown-root.xml", qti.OutcomeInvalid, qti.CodeUnsupportedDocument, "", "qti-item-body"},
	}
	v := testValidator(t)
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			res := validateFile(t, v, tt.file)
			if res.Valid || res.Outcome != tt.outcome || res.Schema != tt.schema {
				t.Fatalf("got valid=%v outcome=%v schema=%q, want outcome %v schema %q: %+v",
					res.Valid, res.Outcome, res.Schema, tt.outcome, tt.schema, res.Errors)
			}
			if len(res.Errors) == 0 || res.Errors[0].Code != tt.code {
				t.Fatalf("want first error code %q, got %+v", tt.code, res.Errors)
			}
			if !strings.Contains(res.Errors[0].Message, tt.message) {
				t.Fatalf("message %q does not mention %q", res.Errors[0].Message, tt.message)
			}
			if tt.code == qti.CodeValidation && (res.Errors[0].Line == 0 || res.Errors[0].Column == 0) {
				t.Fatalf("validation error without position: %+v", res.Errors[0])
			}
		})
	}
}

func TestMultipleValidationErrors(t *testing.T) {
	res := validateFile(t, testValidator(t), "invalid/multiple-errors.xml")
	if res.Outcome != qti.OutcomeInvalid || len(res.Errors) < 3 {
		t.Fatalf("want at least 3 errors, got %+v", res)
	}
}

func TestMaxErrors(t *testing.T) {
	limits := testValidator(t).Limits()
	limits.MaxErrors = 1
	res := validateFile(t, testValidator(t).WithLimits(limits), "invalid/multiple-errors.xml")
	if res.Outcome != qti.OutcomeInvalid || len(res.Errors) != 1 {
		t.Fatalf("want exactly 1 error, got %+v", res.Errors)
	}
}

// Warnings of the rules do not use up MAX_ERRORS: three warnings before an
// error leave room for the error, however small MAX_ERRORS is. Warnings
// have a limit of their own, MAX_ERRORS; one more warning marks the cut.
func TestMaxErrorsCountsErrorsOnly(t *testing.T) {
	cond := `<qti-response-condition><qti-response-if>
      <qti-match><qti-variable identifier="R2"/><qti-correct identifier="R2"/></qti-match>
      <qti-set-outcome-value identifier="SCORE"><qti-base-value base-type="float">1</qti-base-value></qti-set-outcome-value>
    </qti-response-if></qti-response-condition>`
	// R2 has no correct response: a warning per qti-correct. R3 is a string,
	// which a choice interaction cannot be bound to: an error.
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<qti-assessment-item xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0" identifier="i" title="t" time-dependent="false">
  <qti-response-declaration identifier="R2" cardinality="single" base-type="identifier"/>
  <qti-response-declaration identifier="R3" cardinality="single" base-type="string"/>
  <qti-outcome-declaration identifier="SCORE" cardinality="single" base-type="float"/>
  <qti-item-body>
    <qti-choice-interaction response-identifier="R3" max-choices="1">
      <qti-simple-choice identifier="A">A</qti-simple-choice>
    </qti-choice-interaction>
  </qti-item-body>
  <qti-response-processing>` + strings.Repeat(cond, 3) + `</qti-response-processing>
</qti-assessment-item>`
	for _, tc := range []struct {
		maxErrors, warnings int
		note                string // the last warning, when warnings were left out
	}{
		{100, 3, ""},
		{3, 3, ""},
		{2, 2, "1 further warning of the rules was left out: only the first 2 are reported (MAX_ERRORS)."},
		{1, 1, "2 further warnings of the rules were left out: only the first 1 are reported (MAX_ERRORS)."},
	} {
		v := testValidator(t).WithLimits(qti.Limits{MaxErrors: tc.maxErrors})
		res := v.ValidateDocument(context.Background(), app.ValidateDocument{Document: strings.NewReader(doc)})
		warnings, note := res.Warnings, ""
		if n := len(warnings); n > 0 && warnings[n-1].Code == qti.CodeLimitExceeded {
			warnings, note = warnings[:n-1], warnings[n-1].Message
		}
		if res.Valid || res.Outcome != qti.OutcomeInvalid || len(res.Errors) != 1 || len(warnings) != tc.warnings || note != tc.note ||
			!strings.HasSuffix(res.Errors[0].Rule, "#interaction-response-type") {
			t.Errorf("MAX_ERRORS %d: valid=%v, %d errors, %d warnings, note %q; want 1 error, %d warnings, note %q: %+v",
				tc.maxErrors, res.Valid, len(res.Errors), len(warnings), note, tc.warnings, tc.note, res.Errors)
		}
	}
}

func TestDocumentTooLarge(t *testing.T) {
	limits := testValidator(t).Limits()
	limits.MaxDocumentSize = 200
	res := validateFile(t, testValidator(t).WithLimits(limits), "valid/assessment-item.xml")
	if res.Outcome != qti.OutcomeTooLarge || res.Errors[0].Code != qti.CodeTooLarge {
		t.Fatalf("got %+v", res)
	}
}

func TestDepthLimit(t *testing.T) {
	limits := testValidator(t).Limits()
	limits.MaxDepth = 3
	res := validateFile(t, testValidator(t).WithLimits(limits), "valid/assessment-item.xml")
	if res.Valid || res.Errors[0].Code != qti.CodeLimitExceeded {
		t.Fatalf("got %+v", res)
	}
}

func TestRejectsDTDAndExternalEntities(t *testing.T) {
	docs := map[string]string{
		"external entity": `<?xml version="1.0"?>
<!DOCTYPE qti-assessment-item [<!ENTITY xxe SYSTEM "file:///etc/passwd">]>
<qti-assessment-item xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0" identifier="x" title="&xxe;" time-dependent="false"/>`,
		"entity expansion": `<!DOCTYPE a [<!ENTITY a "aaaaaaaaaa"><!ENTITY b "&a;&a;&a;&a;&a;&a;&a;&a;&a;&a;">]>
<qti-assessment-item xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0">&b;</qti-assessment-item>`,
	}
	v := testValidator(t)
	for name, doc := range docs {
		t.Run(name, func(t *testing.T) {
			res := v.ValidateDocument(context.Background(), app.ValidateDocument{Document: strings.NewReader(doc)})
			if res.Valid || res.Errors[0].Code != qti.CodeUnsupportedXML {
				t.Fatalf("got %+v", res)
			}
		})
	}
}

func TestRejectsNonUTF8(t *testing.T) {
	doc := `<?xml version="1.0" encoding="ISO-8859-1"?><qti-assessment-item xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0"/>`
	res := testValidator(t).ValidateDocument(context.Background(), app.ValidateDocument{Document: strings.NewReader(doc)})
	if res.Valid || res.Errors[0].Code != qti.CodeUnsupportedEncoding {
		t.Fatalf("got %+v", res)
	}
}

// The XSD library carries the text of a parse error in its cause.
func TestMalformedMessageIsNotEmpty(t *testing.T) {
	doc := readTestdata(t, "valid/assessment-item.xml")
	res := validateString(t, doc[:len(doc)-30], "")
	if res.Outcome != qti.OutcomeMalformed || res.Errors[0].Message == "" || res.Errors[0].Line == 0 {
		t.Fatalf("got %+v", res.Errors)
	}
}

func TestMalformedWithUnknownRootIsMalformed(t *testing.T) {
	res := testValidator(t).ValidateDocument(context.Background(), app.ValidateDocument{Document: strings.NewReader(`<a><b></a>`)})
	if res.Outcome != qti.OutcomeMalformed || res.Errors[0].Code != qti.CodeInvalidXML {
		t.Fatalf("got %+v", res)
	}
}

func TestEmptyBody(t *testing.T) {
	res := testValidator(t).ValidateDocument(context.Background(), app.ValidateDocument{Document: strings.NewReader("")})
	if res.Outcome != qti.OutcomeMalformed {
		t.Fatalf("got %+v", res)
	}
}

func TestConcurrentValidation(t *testing.T) {
	v := testValidator(t)
	valid, err := os.ReadFile("../../testdata/valid/assessment-item.xml")
	if err != nil {
		t.Fatal(err)
	}
	invalid, err := os.ReadFile("../../testdata/invalid/invalid-value.xml")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			doc, want := valid, true
			if i%2 == 1 {
				doc, want = invalid, false
			}
			if res := v.ValidateDocument(context.Background(), app.ValidateDocument{Document: strings.NewReader(string(doc))}); res.Valid != want {
				t.Errorf("goroutine %d: got %+v", i, res)
			}
		}()
	}
	wg.Wait()
}

func TestEveryDocumentTypeIsDeclared(t *testing.T) {
	v := testValidator(t)
	for _, dt := range qti.DocumentTypes() {
		doc := `<` + dt.Root + ` xmlns="` + dt.Namespace + `"/>`
		res := v.ValidateDocument(context.Background(), app.ValidateDocument{Document: strings.NewReader(doc)})
		for _, e := range res.Errors {
			if e.Code == qti.CodeUnsupportedDocument || e.Code == qti.CodeUnsupportedXML ||
				strings.Contains(e.Message, "no declaration") || strings.Contains(e.Message, "not declared") {
				t.Errorf("%s: root is not declared in the schemas: %+v", dt.Root, e)
			}
		}
	}
}
