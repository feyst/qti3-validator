package app_test

import (
	"slices"
	"strings"
	"testing"

	"qti3-validator/internal/domain/qti"
)

func TestSchematronUnknownAttribute(t *testing.T) {
	res := validateFile(t, testValidator(t), "invalid/schematron-unknown-attribute.xml")
	if res.Valid || res.Outcome != qti.OutcomeInvalid || len(res.Errors) != 1 {
		t.Fatalf("want one error, got %+v", res)
	}
	e := res.Errors[0]
	// colour is rejected; data-colour is an allowed extension attribute.
	if e.Code != qti.CodeSchematron || !strings.Contains(e.Message, "colour") || strings.Contains(e.Message, "data-colour") ||
		e.Rule == "" || e.Line != 18 || e.Path != "/qti-assessment-item[1]/qti-item-body[1]/qti-choice-interaction[1]" {
		t.Fatalf("got %+v", e)
	}
}

func TestSchematronMinMax(t *testing.T) {
	res := validateFile(t, testValidator(t), "invalid/schematron-min-max.xml")
	if res.Valid || len(res.Errors) != 1 || res.Errors[0].Code != qti.CodeSchematron ||
		!strings.Contains(res.Errors[0].Message, "max-choices") {
		t.Fatalf("got %+v", res)
	}
}

// XSD and Schematron errors are reported together.
func TestSchematronAfterXSDErrors(t *testing.T) {
	doc := strings.Replace(readTestdata(t, "invalid/schematron-unknown-attribute.xml"), `shuffle="false"`, `shuffle="sometimes"`, 1)
	res := validateString(t, doc, "")
	var codes []qti.Code
	for _, e := range res.Errors {
		codes = append(codes, e.Code)
	}
	if !slices.Equal(codes, []qti.Code{qti.CodeValidation, qti.CodeSchematron}) {
		t.Fatalf("got %v", res.Errors)
	}
}
