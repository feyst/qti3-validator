package xpath

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// partsDoc has two parts: the first with n elements ref and n elements
// item, the second with one item and no ref.
func partsDoc(t testing.TB, n int) *Document {
	t.Helper()
	var b strings.Builder
	b.WriteString(`<test><part id="p1"><sec>`)
	for i := range n {
		fmt.Fprintf(&b, `<ref id="r%d" x="%d"/>`, i, i%3)
	}
	for i := range n {
		fmt.Fprintf(&b, `<item id="i%d"><rule/></item>`, i)
	}
	b.WriteString(`</sec></part><part id="p2"><sec><item id="last"><rule/></item></sec></part></test>`)
	doc, err := Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// A path that is asked only whether it selects a node stops at the first:
// a rule that asks, at each of n elements, whether a subtree holds any of
// m others takes a few steps each time, not m. Its value is that of plain
// evaluation.
func TestExistenceStopsAtTheFirstNode(t *testing.T) {
	const n = 500
	doc := partsDoc(t, n)
	rules, err := Compile("//rule", Options{})
	if err != nil {
		t.Fatal(err)
	}
	contexts, err := rules.Select(Context{Node: doc.Root})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { optimise = true }()
	for _, tc := range []struct {
		src  string
		want float64 // of the n+1 rules, how many the expression holds for
		few  bool    // answered at the first node
	}{
		{"ancestor::part[1]//ref", n, true},
		{"boolean(ancestor::part[1]//ref)", n, true},
		{"not(ancestor::part[1]//ref)", 1, true},
		{"@none or ancestor::part[1]//ref", n, true},
		{"ancestor::part[1]//ref and ..", n, true},
		{"ancestor::part[1]//ref[@x = '1']", n, true},
		{"ancestor::part[1]/sec/ref", n, true},
		{"ancestor::part[1]/sec/*[@x]", n, true},
		{"ancestor::part[1][.//ref]", n, true},
		{"ancestor::part[1][sec[ref]]", n, true},
		{"(ancestor::part[1]//ref)[@x = '2']", n, false}, // a filter: all of them
		{"ancestor::part[1]//ref[last()]", n, false},     // by position: all of them
		{"count(ancestor::part[1]//ref) > 0", n, false},  // counted: all of them
		{"ancestor::part[1]//ref[@x = '7']", 0, false},   // none: every one is looked at
		{"ancestor::part[1]//none", 0, true},
	} {
		var steps [2]int64
		for i, on := range []bool{false, true} {
			optimise = on
			e, err := Compile(tc.src, Options{})
			if err != nil {
				t.Fatalf("%s: %v", tc.src, err)
			}
			s := NewSession(context.Background(), testBudget)
			held := 0.0
			for _, c := range contexts {
				ok, err := e.EvaluateBool(Context{Node: c, Session: s})
				if err != nil {
					t.Fatalf("%s: %v", tc.src, err)
				}
				if ok {
					held++
				}
			}
			if held != tc.want {
				t.Errorf("optimise %v, %s: holds for %v rules, want %v", on, tc.src, held, tc.want)
			}
			steps[i] = stepsUsed(s)
		}
		if few := steps[1] < 40*n; few != tc.few {
			t.Errorf("%s: %d steps for %d rules (%d without the shortcut), want few: %v", tc.src, steps[1], n+1, steps[0], tc.few)
		}
	}
}

// A path whose last predicate could raise a type error is evaluated in
// full when asked whether it selects a node, so the error is the result as
// in plain evaluation.
func TestExistenceKeepsErrors(t *testing.T) {
	doc, err := Parse(strings.NewReader(`<r><a x="1"/><a x="2"/></r>`))
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Variables: map[string]bool{"s": true}}
	vars := map[string]Value{"s": "str"}
	defer func() { optimise = true }()
	for _, src := range []string{
		"boolean(/r/a[@x = '1' or $s/x])", "not(//a[@x = '1' or count($s)])", "//a[@x = '1' or $s/x] or false()",
		"/r[a[@x = '1' or $s/x]]", "boolean(/r/a[@x = '1' or string($s)])", "boolean(/r/a[@x = $s])",
	} {
		var values [2]Value
		var errs [2]error
		for i, on := range []bool{false, true} {
			optimise = on
			e, err := Compile(src, opts)
			if err != nil {
				t.Fatal(err)
			}
			values[i], errs[i] = e.Evaluate(Context{Node: doc.Root, Variables: vars})
		}
		if (errs[0] != nil) != (errs[1] != nil) || !sameValue(values[0], values[1]) {
			t.Errorf("%s: got %v, %v; want %v, %v", src, values[1], errs[1], values[0], errs[0])
		}
	}
}

// Paths counts the siblings of a parent once for all the paths asked of
// its children in document order: without what it remembers per parent,
// every path would count the siblings before its node again.
func TestPathsCountSiblingsOnce(t *testing.T) {
	const n = 5000
	doc, err := Parse(strings.NewReader("<r><s>" + strings.Repeat("<a/><b/>", n) + "</s></r>"))
	if err != nil {
		t.Fatal(err)
	}
	children := doc.Root.Children[0].Children[0].Children
	var p Paths
	for _, c := range children {
		p.Path(c)
	}
	// Every sibling once, and the two ancestors.
	if p.visited > len(children)+2 {
		t.Errorf("%d siblings counted for the paths of %d, want each once", p.visited, len(children))
	}
	// Asked again, the paths are remembered.
	before := p.visited
	for _, c := range children {
		p.Path(c)
	}
	if p.visited != before {
		t.Errorf("%d more siblings counted for paths asked before", p.visited-before)
	}
	// Attributes and text ask for the path of their element.
	doc, err = Parse(strings.NewReader("<r>" + strings.Repeat(`<a x="1">t</a>`, n) + "</r>"))
	if err != nil {
		t.Fatal(err)
	}
	var q Paths
	for _, c := range doc.Root.Children[0].Children {
		q.Path(c.Attrs[0])
		q.Path(c.Children[0])
	}
	if q.visited > n+1 {
		t.Errorf("%d siblings counted for the attributes and texts of %d elements", q.visited, n)
	}
}
