package rules

import (
	"context"
	"errors"
	"strings"
	"testing"

	"qti3-validator/internal/app"
	"qti3-validator/internal/domain/qti"
	"qti3-validator/internal/lib/xpath"
)

// invalidValues is an item with n values that are no integers and, when
// undeclared, n interactions without a declaration, which the rule sets
// report before the type checks run.
func invalidValues(n int, undeclared bool) string {
	body := `<p>x</p>`
	if undeclared {
		body = strings.Repeat(`<p><qti-text-entry-interaction response-identifier="NONE"/></p>`, n)
	}
	return `<qti-assessment-item xmlns="` + qti.NamespaceASI + `" identifier="i" title="t" adaptive="false" time-dependent="false">` +
		`<qti-response-declaration identifier="RESPONSE" cardinality="multiple" base-type="integer"><qti-correct-response>` +
		strings.Repeat(`<qti-value>x</qti-value>`, n) + `</qti-correct-response></qti-response-declaration>` +
		`<qti-item-body>` + body + `</qti-item-body></qti-assessment-item>`
}

func countCodes(findings []app.RuleFinding) (types, others int) {
	for _, f := range findings {
		if f.Code == qti.CodeValueType {
			types++
		} else if !f.Warning {
			others++
		}
	}
	return types, others
}

// The type checks stop at the limit on errors, which they share with the
// rule sets before them: they work out no more findings than are kept.
func TestTypeChecksStopAtTheLimit(t *testing.T) {
	c := versionRules(t, "3.0.1")
	for _, tc := range []struct {
		name          string
		doc           string
		limit         int
		types, others int
	}{
		{"no limit", invalidValues(300, false), 0, 300, 0},
		{"a limit", invalidValues(300, false), 7, 7, 0},
		{"a limit above the findings", invalidValues(5, false), 7, 5, 0},
		{"room left by the rule sets", invalidValues(10, true), 14, 4, 10},
		{"no room left by the rule sets", invalidValues(10, true), 10, 0, 10},
		{"rule sets beyond the limit", invalidValues(10, true), 4, 0, 4},
	} {
		findings, err := c.CheckRules(context.Background(), []byte(tc.doc), app.RuleLimits{Errors: tc.limit})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if types, others := countCodes(findings); types != tc.types || others != tc.others {
			t.Errorf("%s: %d type findings and %d others, want %d and %d", tc.name, types, others, tc.types, tc.others)
		}
	}
	// The findings kept are the first, with the paths Node.Path gives.
	findings, err := c.CheckRules(context.Background(), []byte(invalidValues(300, false)), app.RuleLimits{Errors: 3})
	if err != nil || len(findings) != 3 {
		t.Fatalf("got %d findings, %v", len(findings), err)
	}
	for i, f := range findings {
		want := "/qti-assessment-item[1]/qti-response-declaration[1]/qti-correct-response[1]/qti-value[" + string(rune('1'+i)) + "]"
		if f.Path != want {
			t.Errorf("finding %d at %s, want %s", i, f.Path, want)
		}
	}
}

// The type checks are charged to the budget of the rules and stop with
// their context, with the findings so far.
func TestTypeChecksStopWithTheSession(t *testing.T) {
	doc, err := xpath.Parse(strings.NewReader(invalidValues(1000, false)))
	if err != nil {
		t.Fatal(err)
	}
	found, err := checkTypes(xpath.NewSession(context.Background(), 100), doc, 0, &xpath.Paths{})
	if !errors.Is(err, xpath.ErrTooComplex) || len(found) == 0 || len(found) >= 100 {
		t.Errorf("a budget of 100 steps: %d findings, %v", len(found), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	found, err = checkTypes(xpath.NewSession(ctx, 0), doc, 0, &xpath.Paths{})
	if !errors.Is(err, context.Canceled) || len(found) != 0 {
		t.Errorf("a context that is done: %d findings, %v", len(found), err)
	}
	// Through CheckRules: the budget of the rules covers the type checks,
	// and running out of it is the same finding as for a rule set.
	c := versionRules(t, "3.0.1")
	c.sets = nil
	c.budget = budget{base: 100}
	findings, err := c.CheckRules(context.Background(), []byte(invalidValues(1000, false)), app.RuleLimits{})
	if !errors.Is(err, app.ErrTooComplex) || len(findings) == 0 {
		t.Errorf("CheckRules on 100 steps: %d findings, %v", len(findings), err)
	}
	// Every element costs a step, an expression as much as a value.
	exprs, err := xpath.Parse(strings.NewReader(`<qti-assessment-item xmlns="` + qti.NamespaceASI + `" identifier="i">` +
		`<qti-outcome-declaration identifier="SCORE" cardinality="single" base-type="float"/><qti-response-processing>` +
		`<qti-set-outcome-value identifier="SCORE"><qti-sum>` + strings.Repeat(`<qti-base-value base-type="float">1</qti-base-value>`, 1000) +
		`</qti-sum></qti-set-outcome-value></qti-response-processing></qti-assessment-item>`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checkTypes(xpath.NewSession(context.Background(), 500), exprs, 0, &xpath.Paths{}); !errors.Is(err, xpath.ErrTooComplex) {
		t.Errorf("1,000 operands on 500 steps: %v", err)
	}
	if _, err := checkTypes(xpath.NewSession(context.Background(), 1100), exprs, 0, &xpath.Paths{}); err != nil {
		t.Errorf("1,000 operands on 1,100 steps: %v", err)
	}
}
