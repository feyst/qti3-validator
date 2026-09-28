package validator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kennisnet/qti3-validator/internal/xpath"
)

func TestSchematronUnknownAttribute(t *testing.T) {
	res := validateFile(t, testValidator(t), "invalid/schematron-unknown-attribute.xml")
	if res.Valid || res.Outcome != OutcomeInvalid || len(res.Errors) != 1 {
		t.Fatalf("want one error, got %+v", res)
	}
	e := res.Errors[0]
	// colour is rejected; data-colour is an allowed extension attribute.
	if e.Code != CodeSchematron || !strings.Contains(e.Message, "colour") || strings.Contains(e.Message, "data-colour") ||
		e.Rule == "" || e.Line != 18 || e.Path != "/qti-assessment-item[1]/qti-item-body[1]/qti-choice-interaction[1]" {
		t.Fatalf("got %+v", e)
	}
}

func TestSchematronMinMax(t *testing.T) {
	res := validateFile(t, testValidator(t), "invalid/schematron-min-max.xml")
	if res.Valid || len(res.Errors) != 1 || res.Errors[0].Code != CodeSchematron ||
		!strings.Contains(res.Errors[0].Message, "max-choices") {
		t.Fatalf("got %+v", res)
	}
}

// XSD and Schematron errors are reported together.
func TestSchematronAfterXSDErrors(t *testing.T) {
	src, err := os.ReadFile("../../testdata/invalid/schematron-unknown-attribute.xml")
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.Replace(string(src), `shuffle="false"`, `shuffle="sometimes"`, 1)
	res := testValidator(t).Validate(t.Context(), strings.NewReader(doc), ValidateOptions{})
	var codes []string
	for _, e := range res.Errors {
		codes = append(codes, e.Code)
	}
	if !slices.Equal(codes, []string{CodeValidation, CodeSchematron}) {
		t.Fatalf("got %v", res.Errors)
	}
}

// TestSchematronMatchesReference checks the embedded rules against results
// of the ISO Schematron reference implementation (the XSLT skeleton, run by
// lxml 5 / libxml2 2.14.6 / libxslt) on generated documents. The documents
// were made by testdata/schematron/generate.py and chosen to fire 68
// different QTI rules; the reference ran the QTI 3.0.0 and LOM rules, so the
// findings of the validator's own rules are left out here.
func TestSchematronMatchesReference(t *testing.T) {
	data, err := os.ReadFile("../../testdata/schematron/expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string][]string
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	rules := testValidator(t).versions["3.0.0"].rules
	for name, want := range expected {
		f, err := os.Open(filepath.Join("../../testdata/schematron/docs", name))
		if err != nil {
			t.Fatal(err)
		}
		doc, err := xpath.Parse(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		failures, err := rules.Validate(doc, 0)
		if err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, fl := range failures {
			if fl.Source == AdditionalRules {
				continue
			}
			got = append(got, strings.Join(strings.Fields(fl.Message), " "))
		}
		slices.Sort(got)
		if want == nil {
			want = []string{}
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s:\n got  %q\n want %q", name, got, want)
		}
	}
}
