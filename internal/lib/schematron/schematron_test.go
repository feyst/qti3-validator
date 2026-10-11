package schematron

import (
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"qti3-validator/internal/lib/xpath"
)

func compileFeatures(t testing.TB) *Engine {
	const name = "testdata/features.sch"
	t.Helper()
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rs, err := Extract(name, f)
	if err != nil {
		t.Fatal(err)
	}
	e, err := Compile(rs)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func parseDoc(t testing.TB, src string) *xpath.Document {
	t.Helper()
	doc, err := xpath.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func validateFile(t *testing.T, e *Engine, name string) []Failure {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	failures, err := e.Validate(nil, parseDoc(t, string(data)), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return failures
}

func TestFeaturesValid(t *testing.T) {
	e := compileFeatures(t)
	if f := validateFile(t, e, "testdata/order-valid.xml"); len(f) != 0 {
		t.Fatalf("want no failures, got %+v", f)
	}
}

func TestFeaturesInvalid(t *testing.T) {
	e := compileFeatures(t)
	var got []string
	for _, f := range validateFile(t, e, "testdata/order-invalid.xml") {
		kind := "error"
		if f.Warning {
			kind = "warning"
		}
		got = append(got, fmt.Sprintf("%s %s %s@%d: %s", kind, f.Pattern, f.Node.Path(), f.Node.Line, f.Message))
	}
	want := []string{
		"warning basic /order[1]@1: Large order: 4 lines.",
		"error basic /order[1]/line[1]@3: Quantity 12 of line exceeds max 10. Lower the quantity of line l1.",
		"error basic /order[1]/line[2]@4: Line l2 has no qty.",
		"error basic /order[1]/ref[1]@7: Reference l9 points to no line.",
		"error basic /order[1]/ref[1]@7: Reference must match exactly one line.",
		"error basic /order[1]/ref[2]@8: Reference must match exactly one line.",
		"error attributes /order[1]/@code@1: Attribute code must have 3 characters, has 'ABCD'.",
		"error attributes /order[1]/note[1]/@lang@9: Attribute lang must have 3 characters, has 'nl'.",
		"error extends /order[1]/customer[1]@2: vip must be a boolean.",
		"error extends /order[1]/customer[1]@2: A customer needs a name.",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestAbstractPatternInstance(t *testing.T) {
	e := compileFeatures(t)
	failures, err := e.Validate(nil, parseDoc(t, `<order xmlns="urn:order" code="ABC"><line id="a" qty="1"/></order>`), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 || failures[0].Pattern != "order-has-customer" ||
		failures[0].Message != "Element order needs o:customer." {
		t.Fatalf("got %+v", failures)
	}
}

// A session that runs out of steps stops the validation, which returns the
// failures found before.
func TestValidateStopsWithinBudget(t *testing.T) {
	const sch = `<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron">
<sch:pattern id="cheap"><sch:rule context="a"><sch:assert test="@ok">no ok</sch:assert></sch:rule></sch:pattern>
<sch:pattern id="quadratic"><sch:rule context="a"><sch:assert test="count(//a[@id = current()/@id]) = 1">dup</sch:assert></sch:rule></sch:pattern>
</sch:schema>`
	rs, err := Extract("x", strings.NewReader(sch))
	if err != nil {
		t.Fatal(err)
	}
	e, err := Compile(rs)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString(`<r><a id="first"/>`)
	for i := range 1000 {
		fmt.Fprintf(&b, `<a ok="1" id="a%d"/>`, i)
	}
	b.WriteString("</r>")
	doc := parseDoc(t, b.String())
	failures, err := e.Validate(xpath.NewSession(t.Context(), 100_000), doc, Limits{})
	if !errors.Is(err, xpath.ErrTooComplex) || len(failures) != 1 || failures[0].Pattern != "cheap" {
		t.Fatalf("got %+v, %v", failures, err)
	}
}

func TestMaxFailures(t *testing.T) {
	e := compileFeatures(t)
	data, _ := os.ReadFile("testdata/order-invalid.xml")
	failures, err := e.Validate(nil, parseDoc(t, string(data)), Limits{Errors: 3, Warnings: -1})
	if err != nil || len(failures) != 3 {
		t.Fatalf("got %d failures, %v", len(failures), err)
	}
}

// Warnings do not count towards the limit on errors: five warnings before
// an error leave room for the error. They have a limit of their own, and
// the warnings beyond it are counted.
func TestLimitsCountErrorsAndWarningsApart(t *testing.T) {
	const sch = `<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron">
<sch:pattern id="w"><sch:rule context="a"><sch:report test="true()" role="warning">w</sch:report></sch:rule></sch:pattern>
<sch:pattern id="e"><sch:rule context="b"><sch:assert test="false()">e</sch:assert></sch:rule></sch:pattern>
</sch:schema>`
	rs, err := Extract("x", strings.NewReader(sch))
	if err != nil {
		t.Fatal(err)
	}
	e, err := Compile(rs)
	if err != nil {
		t.Fatal(err)
	}
	doc := parseDoc(t, `<r><a/><a/><a/><a/><a/><b/><b/></r>`)
	count := func(failures []Failure) (errs, warnings int) {
		for _, f := range failures {
			if f.Warning {
				warnings++
			} else {
				errs++
			}
		}
		return errs, warnings
	}
	for _, tc := range []struct {
		limits                  Limits
		errs, warnings, omitted int
	}{
		{Limits{}, 2, 5, 0},
		{Limits{Errors: 1}, 1, 5, 0},
		{Limits{Errors: 3}, 2, 5, 0},
		{Limits{Errors: 1, Warnings: 2}, 1, 2, 3},
		{Limits{Warnings: 5}, 2, 5, 0},
		{Limits{Warnings: 4}, 2, 4, 1},
		{Limits{Warnings: -1}, 2, 0, 5},
	} {
		res, err := e.Check(nil, doc, tc.limits)
		if errs, warnings := count(res.Failures); err != nil || errs != tc.errs || warnings != tc.warnings || res.OmittedWarnings != tc.omitted {
			t.Errorf("%+v: got %d errors, %d warnings, %d omitted, %v; want %d, %d, %d",
				tc.limits, errs, warnings, res.OmittedWarnings, err, tc.errs, tc.warnings, tc.omitted)
		}
	}
}

func TestRejectsUnsupported(t *testing.T) {
	const head = `<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron"`
	for name, src := range map[string]string{
		"xslt2 binding":   head + ` queryBinding="xslt2"><sch:pattern><sch:rule context="a"><sch:assert test="1">x</sch:assert></sch:rule></sch:pattern></sch:schema>`,
		"include":         head + `><sch:include href="other.sch"/></sch:schema>`,
		"key()":           head + `><sch:pattern><sch:rule context="a"><sch:assert test="key('k', 'v')">x</sch:assert></sch:rule></sch:pattern></sch:schema>`,
		"unknown prefix":  head + `><sch:pattern><sch:rule context="p:a"><sch:assert test="1">x</sch:assert></sch:rule></sch:pattern></sch:schema>`,
		"undeclared var":  head + `><sch:pattern><sch:rule context="a"><sch:assert test="$nope">x</sch:assert></sch:rule></sch:pattern></sch:schema>`,
		"missing extends": head + `><sch:pattern><sch:rule context="a"><sch:extends rule="r"/></sch:rule></sch:pattern></sch:schema>`,
		"missing is-a":    head + `><sch:pattern is-a="nope" id="p"/></sch:schema>`,
		"missing diag":    head + `><sch:pattern><sch:rule context="a"><sch:assert test="1" diagnostics="d">x</sch:assert></sch:rule></sch:pattern></sch:schema>`,
		"let content":     head + `><sch:let name="x"><foo/></sch:let></sch:schema>`,
	} {
		t.Run(name, func(t *testing.T) {
			rs, err := Extract(name, strings.NewReader(src))
			if err == nil {
				_, err = Compile(rs)
			}
			if err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestContextToXPath(t *testing.T) {
	for in, want := range map[string]string{
		"a":                  "//a",
		"/a":                 "/a",
		"//q:a":              "//q:a",
		"a | b/c":            "//a | //b/c",
		"a[@x='|'] | @y":     "//a[@x='|'] | //@y",
		"x[contains(.,'|')]": "//x[contains(.,'|')]",
	} {
		if got := contextToXPath(in); got != want {
			t.Errorf("contextToXPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// qtiRules extracts the rules from the QTI 3 ASI schema fetched for the
// validator.
func qtiRules(t testing.TB) RuleSet {
	t.Helper()
	path := filepath.Join("..", "validator", "schemas", "purl.imsglobal.org", "spec", "qti", "v3p0", "schema", "xsd", "imsqti_asiv3p0_v1p0.xsd.gz")
	f, err := os.Open(path)
	if err != nil {
		t.Skipf("schemas not fetched: %v", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := Extract("asi", zr)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

// The native attribute-name check must agree with XPath on every assertion
// it replaces, for elements with and without the attribute at that position.
func TestAttributeNameCheckMatchesXPath(t *testing.T) {
	rs := qtiRules(t)
	docs := []*xpath.Document{
		parseDoc(t, `<e/>`),
		parseDoc(t, `<e xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:schemaLocation="x" identifier="i" title="t" xml:lang="nl" class="c" id="x" data-x="1" colour="red" label="l" shuffle="true" max-choices="1" response-identifier="R" data-="" zzz="1"/>`),
		parseDoc(t, `<e colour="red" identifier="i" title="t" a1="" a2="" a3="" a4="" a5="" a6="" a7="" a8="" a9="" a10="" a11="" a12="" a13="" a14="" a15="" a16="" a17="" a18="" a19="" a20="" data-a="" data-b="" data-c="" class="c"/>`),
	}
	native, viaXPath := 0, 0
	for _, p := range rs.Patterns {
		for _, r := range p.Rules {
			for _, a := range r.Assertions {
				check := parseAttributeNameCheck(a.Test)
				if check == nil {
					viaXPath++
					continue
				}
				native++
				expr, err := xpath.Compile(a.Test, xpath.Options{Namespaces: rs.Namespaces})
				if err != nil {
					t.Fatal(err)
				}
				for _, doc := range docs {
					el := doc.Root.Children[0]
					want, err := expr.EvaluateBool(xpath.Context{Node: el})
					if err != nil {
						t.Fatal(err)
					}
					names := map[string]bool{}
					for _, n := range check.names {
						names[n] = true
					}
					rt := &nameCheck{position: check.position, empty: check.empty, names: names, prefixes: check.prefixes}
					if got := rt.holds(el); got != want {
						t.Fatalf("%s on %s: native %v, XPath %v", a.Test[:80], el.Path(), got, want)
					}
				}
			}
		}
	}
	t.Logf("%d assertions native, %d via XPath", native, viaXPath)
	if native < 6000 {
		t.Fatalf("only %d assertions recognised as attribute-name checks", native)
	}
}

func TestQTIRulesCompile(t *testing.T) {
	rs := qtiRules(t)
	c, err := Precompile(rs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(c); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d shared name lists", len(c.NameLists))
}
