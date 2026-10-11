package schematron

import (
	"strings"
	"testing"
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
