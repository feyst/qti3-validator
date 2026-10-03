package rules

import (
	"slices"
	"strings"
	"testing"

	"github.com/kennisnet/qti3-validator/internal/domain/qti"
	"github.com/kennisnet/qti3-validator/internal/lib/xpath"
)

func typeFindings(t *testing.T, body string) []string {
	t.Helper()
	doc, err := xpath.Parse(strings.NewReader(`<qti-assessment-item xmlns="` + qti.NamespaceASI + `" identifier="i">
  <qti-response-declaration identifier="RESPONSE" cardinality="single" base-type="identifier">
    <qti-correct-response><qti-value>A</qti-value></qti-correct-response>
  </qti-response-declaration>
  <qti-response-declaration identifier="R_INT" cardinality="single" base-type="integer"/>
  <qti-response-declaration identifier="R_MULTI" cardinality="multiple" base-type="identifier"/>
  <qti-outcome-declaration identifier="SCORE" cardinality="single" base-type="float"/>
  <qti-outcome-declaration identifier="FEEDBACK" cardinality="single" base-type="identifier"/>
` + body + `
</qti-assessment-item>`))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range checkTypes(doc) {
		out = append(out, f.Rule+": "+f.Message)
	}
	slices.Sort(out)
	return out
}

func TestTypeChecksAcceptValidItem(t *testing.T) {
	got := typeFindings(t, `<qti-response-processing>
  <qti-response-condition>
    <qti-response-if>
      <qti-and>
        <qti-match><qti-variable identifier="RESPONSE"/><qti-correct identifier="RESPONSE"/></qti-match>
        <qti-gte><qti-variable identifier="R_INT"/><qti-base-value base-type="integer">2</qti-base-value></qti-gte>
        <qti-member><qti-variable identifier="RESPONSE"/><qti-variable identifier="R_MULTI"/></qti-member>
      </qti-and>
      <qti-set-outcome-value identifier="SCORE"><qti-sum><qti-variable identifier="R_INT"/><qti-base-value base-type="integer">1</qti-base-value></qti-sum></qti-set-outcome-value>
      <qti-set-outcome-value identifier="FEEDBACK"><qti-base-value base-type="identifier">OK</qti-base-value></qti-set-outcome-value>
    </qti-response-if>
  </qti-response-condition>
  <qti-set-outcome-value identifier="SCORE"><qti-custom-operator class="x"><qti-variable identifier="RESPONSE"/></qti-custom-operator></qti-set-outcome-value>
</qti-response-processing>`)
	if len(got) != 0 {
		t.Fatalf("got %q", got)
	}
}

func TestTypeChecksReportMismatches(t *testing.T) {
	got := typeFindings(t, `<qti-response-processing>
  <qti-response-condition>
    <qti-response-if>
      <qti-variable identifier="R_INT"/>
      <qti-set-outcome-value identifier="FEEDBACK"><qti-variable identifier="R_INT"/></qti-set-outcome-value>
    </qti-response-if>
  </qti-response-condition>
  <qti-set-outcome-value identifier="SCORE">
    <qti-sum><qti-variable identifier="RESPONSE"/><qti-base-value base-type="float">x</qti-base-value></qti-sum>
  </qti-set-outcome-value>
  <qti-response-condition>
    <qti-response-if>
      <qti-match><qti-variable identifier="RESPONSE"/><qti-variable identifier="R_INT"/></qti-match>
    </qti-response-if>
  </qti-response-condition>
</qti-response-processing>`)
	want := []string{
		"assignment-type: qti-set-outcome-value sets FEEDBACK [identifier, single] to qti-variable [integer, single].",
		"expression-type: qti-match needs operands of the same type, but qti-variable gives [identifier, single] and qti-variable gives [integer, single].",
		"expression-type: qti-response-if needs a [boolean, single] value, but qti-variable gives [integer, single].",
		"expression-type: qti-sum needs numerical values, but qti-variable gives [identifier, single].",
		`value-type: qti-base-value: "x" is not a valid float value.`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

func TestValueChecks(t *testing.T) {
	doc, err := xpath.Parse(strings.NewReader(`<qti-assessment-item xmlns="` + qti.NamespaceASI + `" identifier="i">
  <qti-response-declaration identifier="R" cardinality="multiple" base-type="directedPair">
    <qti-correct-response><qti-value>A B</qti-value><qti-value>A</qti-value></qti-correct-response>
    <qti-mapping default-value="0"><qti-map-entry map-key="A 1" mapped-value="1"/></qti-mapping>
  </qti-response-declaration>
  <qti-outcome-declaration identifier="PASS" cardinality="single" base-type="boolean"><qti-default-value><qti-value/></qti-default-value></qti-outcome-declaration>
</qti-assessment-item>`))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range checkTypes(doc) {
		got = append(got, f.Message)
	}
	if len(got) != 2 || !strings.Contains(got[0], `"A" is not a valid directedPair`) || !strings.Contains(got[1], `map-key "A 1"`) {
		t.Fatalf("got %q", got)
	}
}
