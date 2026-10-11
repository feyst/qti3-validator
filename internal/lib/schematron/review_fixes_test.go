package schematron

import (
	"context"
	"errors"
	"strings"
	"testing"

	"qti3-validator/internal/lib/xpath"
)

func compileSchema(t *testing.T, sch string) *Engine {
	t.Helper()
	rs, err := Extract("x", strings.NewReader(sch))
	if err != nil {
		t.Fatal(err)
	}
	e, err := Compile(rs)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// Each instance of an abstract pattern extends its own copy of the
// pattern's abstract rules, with its own parameters.
func TestAbstractRulesArePerPatternInstance(t *testing.T) {
	const sch = `<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron">
<sch:pattern id="base" abstract="true">
  <sch:rule id="ar" abstract="true"><sch:assert test="@$attr">missing $attr</sch:assert></sch:rule>
  <sch:rule context="$el"><sch:extends rule="ar"/></sch:rule>
</sch:pattern>
<sch:pattern id="p1" is-a="base"><sch:param name="el" value="a"/><sch:param name="attr" value="x"/></sch:pattern>
<sch:pattern id="p2" is-a="base"><sch:param name="el" value="b"/><sch:param name="attr" value="y"/></sch:pattern>
</sch:schema>`
	rs, err := Extract("x", strings.NewReader(sch))
	if err != nil {
		t.Fatal(err)
	}
	c, err := Precompile(rs)
	if err != nil {
		t.Fatal(err)
	}
	var tests []string
	for _, p := range c.Sets[0].Patterns {
		for _, r := range p.Rules {
			for _, a := range r.Assertions {
				tests = append(tests, p.ID+" "+r.Context+" "+a.Test)
			}
		}
	}
	if got, want := strings.Join(tests, "; "), "p1 //a @x; p2 //b @y"; got != want {
		t.Errorf("compiled to %s, want %s", got, want)
	}
	e, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	failures, err := e.Validate(nil, parseDoc(t, `<r><a x="1"/><b y="1"/></r>`), Limits{})
	if err != nil || len(failures) != 0 {
		t.Errorf("a valid document: got %+v, %v", failures, err)
	}
	failures, err = e.Validate(nil, parseDoc(t, `<r><a y="1"/><b x="1"/></r>`), Limits{})
	if err != nil || len(failures) != 2 || failures[0].Pattern != "p1" || failures[1].Pattern != "p2" {
		t.Errorf("an invalid document: got %+v, %v", failures, err)
	}
}

// A rule may extend an abstract rule of another pattern while one pattern
// defines it; when several do and the rule's own does not, it is not
// guessed which is meant.
func TestAbstractRuleOfAnotherPattern(t *testing.T) {
	const shared = `<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron">
<sch:pattern id="lib"><sch:rule id="ar" abstract="true"><sch:assert test="@x">no x</sch:assert></sch:rule></sch:pattern>
<sch:pattern id="p"><sch:rule context="a"><sch:extends rule="ar"/></sch:rule></sch:pattern>
</sch:schema>`
	e := compileSchema(t, shared)
	failures, err := e.Validate(nil, parseDoc(t, `<r><a/><a x="1"/></r>`), Limits{})
	if err != nil || len(failures) != 1 || failures[0].Message != "no x" {
		t.Errorf("got %+v, %v", failures, err)
	}

	const ambiguous = `<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron">
<sch:pattern id="base" abstract="true">
  <sch:rule id="ar" abstract="true"><sch:assert test="@$attr">missing</sch:assert></sch:rule>
</sch:pattern>
<sch:pattern id="p1" is-a="base"><sch:param name="attr" value="x"/></sch:pattern>
<sch:pattern id="p2" is-a="base"><sch:param name="attr" value="y"/></sch:pattern>
<sch:pattern id="p"><sch:rule context="a"><sch:extends rule="ar"/></sch:rule></sch:pattern>
</sch:schema>`
	rs, err := Extract("x", strings.NewReader(ambiguous))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Compile(rs); err == nil || !strings.Contains(err.Error(), "several patterns") {
		t.Errorf("got %v, want an error for an ambiguous abstract rule", err)
	}
}

// Within a pattern the failures are in document order, whatever the order
// of the rules, so a limit on errors keeps the first in the document.
func TestFailuresAreInDocumentOrderWithinAPattern(t *testing.T) {
	const sch = `<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron">
<sch:pattern id="p">
  <sch:rule context="b"><sch:assert test="false()">b</sch:assert></sch:rule>
  <sch:rule context="a | @k"><sch:assert test="false()">a<sch:value-of select="@n"/></sch:assert><sch:report test="true()">again</sch:report></sch:rule>
  <sch:rule context="*"><sch:assert test="not(self::c)">other</sch:assert></sch:rule>
</sch:pattern>
<sch:pattern id="q">
  <sch:rule context="c"><sch:assert test="false()">q</sch:assert></sch:rule>
</sch:pattern></sch:schema>`
	e := compileSchema(t, sch)
	doc := parseDoc(t, `<r><a n="1"/><b k="v"><a n="2"/></b><c/><b/><a n="3"/></r>`)
	messages := func(limits Limits) string {
		failures, err := e.Validate(nil, doc, limits)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, f := range failures {
			out = append(out, f.Message)
		}
		return strings.Join(out, " ")
	}
	// The pattern p in document order, each node by its first matching
	// rule and its assertions in their order; then the pattern q.
	if got, want := messages(Limits{}), "a1 again b a again a2 again other b a3 again q"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := messages(Limits{Errors: 1}), "a1"; got != want {
		t.Errorf("with a limit of one: got %q, want %q", got, want)
	}
	if got, want := messages(Limits{Errors: 4}), "a1 again b a"; got != want {
		t.Errorf("with a limit of four: got %q, want %q", got, want)
	}
}

// What an abstract rule extends is looked up in its own pattern, not in
// the pattern of the rule that extends it.
func TestNestedExtendsResolveInTheirOwnPattern(t *testing.T) {
	messages := func(sch, doc string) string {
		t.Helper()
		failures, err := compileSchema(t, sch).Validate(nil, parseDoc(t, doc), Limits{})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, f := range failures {
			out = append(out, f.Message)
		}
		return strings.Join(out, " ")
	}
	const lib = `<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron">
<sch:pattern id="lib">
  <sch:rule id="k" abstract="true"><sch:assert test="false()">lib-k</sch:assert></sch:rule>
  <sch:rule id="base" abstract="true"><sch:extends rule="k"/><sch:assert test="false()">base</sch:assert></sch:rule>
</sch:pattern>`
	// The pattern p has a k of its own, which base does not mean.
	const override = lib + `
<sch:pattern id="p">
  <sch:rule id="k" abstract="true"><sch:assert test="false()">p-k</sch:assert></sch:rule>
  <sch:rule context="a"><sch:extends rule="base"/></sch:rule>
  <sch:rule context="b"><sch:extends rule="k"/></sch:rule>
</sch:pattern></sch:schema>`
	if got, want := messages(override, `<r><a/><b/></r>`), "base lib-k p-k"; got != want {
		t.Errorf("a pattern with its own k: got %q, want %q", got, want)
	}
	// Another pattern defines k as well: for base, its own pattern's k is
	// the one, though p could not name k itself.
	const third = lib + `
<sch:pattern id="other">
  <sch:rule id="k" abstract="true"><sch:assert test="false()">other-k</sch:assert></sch:rule>
  <sch:rule context="b"><sch:extends rule="k"/></sch:rule>
</sch:pattern>
<sch:pattern id="p">
  <sch:rule context="a"><sch:extends rule="base"/></sch:rule>
</sch:pattern></sch:schema>`
	if got, want := messages(third, `<r><a/><b/></r>`), "other-k base lib-k"; got != want {
		t.Errorf("k in a third pattern: got %q, want %q", got, want)
	}
	// Rules of one id in two patterns that extend each other's namesake
	// are not a cycle; a rule that reaches itself is.
	const sameID = `<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron">
<sch:pattern id="lib"><sch:rule id="k" abstract="true"><sch:assert test="false()">lib-k</sch:assert></sch:rule></sch:pattern>
<sch:pattern id="p">
  <sch:rule id="j" abstract="true"><sch:extends rule="k"/></sch:rule>
  <sch:rule context="a"><sch:extends rule="j"/></sch:rule>
</sch:pattern></sch:schema>`
	if got, want := messages(sameID, `<r><a/></r>`), "lib-k"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	const cycle = `<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron">
<sch:pattern id="lib"><sch:rule id="k" abstract="true"><sch:extends rule="j"/></sch:rule>
  <sch:rule id="j" abstract="true"><sch:extends rule="k"/></sch:rule></sch:pattern>
<sch:pattern id="p"><sch:rule context="a"><sch:extends rule="k"/></sch:rule></sch:pattern></sch:schema>`
	rs, err := Extract("x", strings.NewReader(cycle))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Compile(rs); err == nil || !strings.Contains(err.Error(), "extends itself") {
		t.Errorf("a cycle: got %v", err)
	}
}

// When the context of a later rule of a pattern stops the session, the
// nodes of the rules before it are still checked: their failures are among
// those found so far.
func TestContextErrorKeepsEarlierRules(t *testing.T) {
	const sch = `<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron">
<sch:pattern id="p">
  <sch:rule context="a"><sch:assert test="false()">a fails</sch:assert></sch:rule>
  <sch:rule context="//b[count(//*[count(//*) > 0]) > 0]"><sch:assert test="false()">b fails</sch:assert></sch:rule>
</sch:pattern></sch:schema>`
	e := compileSchema(t, sch)
	doc := parseDoc(t, `<r>`+strings.Repeat(`<a/><b/>`, 50)+`</r>`)
	failures, err := e.Validate(xpath.NewSession(context.Background(), 400), doc, Limits{})
	if !errors.Is(err, xpath.ErrTooComplex) || len(failures) != 50 {
		t.Fatalf("a budget of 400: got %d failures, %v; want the 50 of the first rule and ErrTooComplex", len(failures), err)
	}
	for _, f := range failures {
		if f.Message != "a fails" {
			t.Fatalf("got %q", f.Message)
		}
	}
	// The limit on errors still holds, and the error is still returned.
	failures, err = e.Validate(xpath.NewSession(context.Background(), 400), doc, Limits{Errors: 7})
	if !errors.Is(err, xpath.ErrTooComplex) || len(failures) != 7 {
		t.Errorf("with a limit of 7: got %d failures, %v", len(failures), err)
	}
	failures, err = e.Validate(nil, doc, Limits{})
	if err != nil || len(failures) != 100 || failures[0].Message != "a fails" || failures[1].Message != "b fails" {
		t.Errorf("without a budget: got %d failures, %v", len(failures), err)
	}
}

// Merging the nodes of several rules into document order is charged to the
// session; a pattern whose nodes are in order already, or with one rule,
// pays nothing for it.
func TestMergeOfRulesIsCharged(t *testing.T) {
	const n = 512
	var b strings.Builder
	b.WriteString("<r>")
	for range n / 2 {
		b.WriteString("<a/><b/>")
	}
	b.WriteString("</r>")
	doc := parseDoc(t, b.String())
	steps := func(rules string) int64 {
		t.Helper()
		e := compileSchema(t, `<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron"><sch:pattern id="p">`+rules+`</sch:pattern></sch:schema>`)
		// The least budget that suffices is the number of steps taken.
		lo, hi := int64(0), int64(100*n)
		for lo < hi {
			mid := (lo + hi) / 2
			s := xpath.NewSession(context.Background(), mid)
			if _, err := e.Validate(s, doc, Limits{}); err != nil {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		return lo
	}
	rule := func(context string) string {
		return `<sch:rule context="` + context + `"><sch:assert test="true()">x</sch:assert></sch:rule>`
	}
	merged := steps(rule("/r/b") + rule("/r/a"))
	ordered := steps(rule("/r/*"))
	t.Logf("%d nodes: %d steps for two rules merged, %d for one rule", n, merged, ordered)
	if merged-ordered < n*9 { // log2(512)
		t.Errorf("merging %d nodes cost %d steps more than not merging, want at least %d", n, merged-ordered, n*9)
	}
}
