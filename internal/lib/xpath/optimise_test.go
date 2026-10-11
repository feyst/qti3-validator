package xpath

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// randomDoc returns a document of some n elements in the namespaces urn:q
// and none, nested up to depth deep at random, with identifiers and
// references from a small set so that they repeat, numbers, text, comments
// and processing instructions, and a chain of 80 nested q:x-interaction
// elements somewhere inside.
func randomDoc(t testing.TB, n, depth int, seed uint64) *Document {
	t.Helper()
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	names := []string{"a", "b", "c", "x-interaction", "d"}
	var b strings.Builder
	b.WriteString(`<r xmlns="urn:q" xmlns:x="urn:x" id="root">`)
	count := 0
	chained := false
	var gen func(level int)
	gen = func(level int) {
		for count < n && r.IntN(4) != 0 {
			count++
			name := names[r.IntN(len(names))]
			open := name
			if name == "d" {
				open = `d xmlns=""`
			}
			fmt.Fprintf(&b, "<%s", open)
			if r.IntN(3) != 0 {
				fmt.Fprintf(&b, ` id="i%d"`, r.IntN(12))
			}
			if r.IntN(3) == 0 {
				fmt.Fprintf(&b, ` ref="i%d"`, r.IntN(12))
			}
			if r.IntN(4) == 0 {
				fmt.Fprintf(&b, ` n="%d"`, r.IntN(5))
			}
			if r.IntN(8) == 0 {
				fmt.Fprintf(&b, ` x:id="i%d"`, r.IntN(12))
			}
			b.WriteString(">")
			switch r.IntN(6) {
			case 0:
				fmt.Fprintf(&b, "t%d", r.IntN(3))
			case 1:
				b.WriteString("<!--c-->")
			case 2:
				b.WriteString("<?p i?>")
			}
			if level < depth {
				gen(level + 1)
			}
			if !chained && level == depth/2 && count > n/3 {
				chained = true
				for i := range 80 {
					fmt.Fprintf(&b, `<x-interaction id="i%d"><c id="i%d"/>`, i%12, i%5)
				}
				b.WriteString(`<a id="i1"/><b id="i1" ref="i3"/>`)
				for range 80 {
					b.WriteString(`</x-interaction>`)
				}
			}
			fmt.Fprintf(&b, "</%s>", name)
		}
	}
	for count < n {
		gen(1)
	}
	b.WriteString("</r>")
	doc, err := Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// optimisedExprs exercise each shortcut of the evaluation, and expressions
// close to them that must not take it.
var optimisedExprs = []string{
	// Steps that stop at their first node, on every axis.
	"ancestor::*[1]", "ancestor::q:x-interaction[1]", "ancestor::*[@id][1]", "ancestor::*[@id = 'i3'][1]",
	"ancestor::*[substring(local-name(), string-length(local-name()) - 11) = '-interaction'][1]",
	"ancestor::*[position() = 1]", "ancestor::*[1 = position()]", "ancestor::*[@id][position() = 1]",
	"ancestor-or-self::*[@n > 2][1]", "ancestor-or-self::node()[1]", "parent::*[1]", "self::*[1]",
	"preceding::*[1]", "preceding::q:a[@id = 'i2'][1]", "preceding::node()[1]", "preceding::text()[1]",
	"preceding-sibling::*[1]", "preceding-sibling::node()[1]", "preceding-sibling::*[@ref][1]",
	"following::q:b[1]", "following::*[@id = current()/@ref][1]", "following::comment()[1]",
	"following-sibling::*[1]", "following-sibling::*[@ref][1]", "following-sibling::node()[1]",
	"descendant::q:c[1]", "descendant::*[@id][1]", "descendant::q:c[@id = 'i1'][1]", "descendant-or-self::*[1]",
	"descendant-or-self::q:x-interaction[1]", "child::*[1]", "child::text()[1]", "@*[1]", "attribute::id[1]",
	"namespace::*[1]", "ancestor::*[not(@id)][1]/@id", "ancestor::*[1]/ancestor::*[1]", "count(ancestor::*[@id][1])",
	"ancestor::*[.//q:a][1]", "ancestor::*[q:c[2]][1]", "ancestor::*[contains(@id, '1')][1]",
	// Positional predicates that are not [1], and [1] after them.
	"ancestor::*[2]", "ancestor::*[@id][2]", "ancestor::*[last()]", "ancestor::*[position() < 3][1]",
	"ancestor::*[count(*)][1]", "ancestor::*[string-length(@id)][1]", "ancestor::*[last() - 1]",
	"preceding::*[3]", "following::*[@id][2]", "descendant::*[position() mod 3 = 0][1]", "ancestor::*[$num][1]",
	"ancestor::*[@id][last()]", "ancestor::*[position() = last()]", "ancestor::*[1][@id]", "ancestor::*[1.5]",
	"(ancestor::*)[1]", "(ancestor::*[@id])[1]", "ancestor::*[$s][1]",
	// Paths looked up by an attribute.
	"//q:a[@id = current()/@ref]", "//*[@id = current()/@id]", "//q:a[@id = current()/@n]", "//q:a[@id = $s]",
	"//q:a[@id = $ns]", "//q:a[@id = $num]", "//q:a[@id = true()]", "//q:a[@n = 3]", "//q:a[current()/@ref = @id]",
	"//q:a[1][@id = current()/@ref]", "//q:a[@ref][@id = current()/@ref]", "//q:a[@x:id = current()/@id]",
	"//q:a[@id = current()/@missing]", "//q:a[@id = //q:b/@ref]", "//q:a[@id = current()//@id]",
	"//q:a[@id = concat(current()/@id, '')]", "//*[self::q:a or self::q:b][@id = current()/@id]",
	"//d[@id = current()/@id]", "//q:none[@id = current()/@id]", "count(//*[@ref = current()/@id])",
	"//q:a[@id = current()/@ref]/@id", "//q:a[@id = string(current()/@ref)]", "//q:a[@id = position()]",
	"//q:a[@id = name()]", "//*[@id = current()/../@id]", "//q:c[@id = $big]",
	// Descendant steps from nested nodes.
	"(//q:x-interaction)//q:c", "$nested//q:c", "$nested//q:c/@id", "ancestor::q:x-interaction//q:c[@id]",
	"ancestor::*//*[1]", "ancestor::*/descendant::q:c[2]", "ancestor-or-self::*/descendant-or-self::q:c",
	"//q:x-interaction//q:c[last()]", "count(ancestor::*//q:a)", "ancestor::*//@id", "$nested//text()",
	"(.|@*)//node()", "(..|../@*|../namespace::*)/descendant-or-self::node()", "(ancestor::*|ancestor::*/@*)//q:c",
	// Descendant steps by name.
	"//q:c", "//q:c[3]", "//q:c[last()]", "//q:x-interaction/descendant::q:c[1]", "descendant::q:a",
	"descendant-or-self::q:x-interaction", "//d", "/descendant::q:c[position() mod 5 = 0]", "count(//q:b)",
	"//x:c", "//q:a/descendant::q:b[2]", "..//q:c", "descendant::q:c[last()]",
	// Comparisons with kept node-sets.
	"//q:a[@id = $big]", "$big = //q:b/@ref", "//q:a/@id = //q:b/@ref", "count(//*[@ref = //q:a/@id])",
	"count(//*[@id != //q:a/@id])", "@id = $big", "$big = current()/@ref", "count(//*[@n = //@n])",
	"$big = 'i3'", "$big = 3", "//q:a[@id = //q:b/@ref][@ref = //q:a/@id]",
	// Descendant steps merged with their predicates.
	"//q:a[@id]", "//q:a[@id][2]", "//q:a[2][@id]", "//q:a[position()]", "//q:a[count(*)]", "//q:a[$num]",
	"//q:a[string(@n)]", "//q:a['x']", "//q:a[.//q:c[1]]", "//q:a[last() > 2]", "//*[@n][1]",
	"//q:a[@id][position() = 1]", "//node()[self::comment()]", "//@*[. = 'i1']",
}

// The shortcuts give the results that plain evaluation gives, for every
// expression, at a sample of context nodes of every kind: every seventh
// node, or every seventieth under the race detector.
func TestOptimisedEvaluationMatchesPlain(t *testing.T) {
	doc := randomDoc(t, 500, 9, 1)
	var contexts []*Node
	i := 0
	var collect func(n *Node)
	collect = func(n *Node) {
		if i%(7*slowdown) == 0 {
			contexts = append(contexts, n)
		}
		i++
		for _, a := range n.Attrs {
			collect(a)
		}
		for _, c := range n.Children {
			collect(c)
		}
	}
	collect(doc.Root)
	opts := Options{Namespaces: indexNS, Variables: map[string]bool{"s": true, "ns": true, "num": true, "big": true, "nested": true}}
	vars := func(session *Session) map[string]Value {
		v := map[string]Value{"s": "i3", "num": 2.0}
		for name, src := range map[string]string{"ns": "//q:b/@ref", "big": "//@ref", "nested": "//q:x-interaction"} {
			e, err := Compile(src, Options{Namespaces: indexNS})
			if err != nil {
				t.Fatal(err)
			}
			if v[name], err = e.Evaluate(Context{Node: doc.Root, Session: session}); err != nil {
				t.Fatal(err)
			}
		}
		if len(v["big"].(NodeSet)) < minSetSize {
			t.Fatal("$big is too small to be kept")
		}
		return v
	}
	for _, src := range optimisedExprs {
		optimise = false
		plain, err := Compile(src, opts)
		optimise = true
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		fast, err := Compile(src, opts)
		if err != nil {
			t.Fatal(err)
		}
		session := NewSession(context.Background(), 0)
		fastVars := vars(session)
		for name, v := range fastVars {
			if name != "nested" {
				session.Constant(v)
			}
		}
		optimise = false
		plainVars := vars(nil)
		optimise = true
		for _, n := range contexts {
			optimise = false
			want, wantErr := plain.Evaluate(Context{Node: n, Variables: plainVars})
			optimise = true
			got, gotErr := fast.Evaluate(Context{Node: n, Variables: fastVars, Session: session})
			if (wantErr != nil) != (gotErr != nil) || !sameValue(got, want) {
				t.Fatalf("%s at %s: got %v, %v; want %v, %v", src, n.Path(), got, gotErr, want, wantErr)
			}
		}
	}
}

// Which steps stop at their first node, and which paths are looked up.
func TestShortcutsApply(t *testing.T) {
	first := map[string]bool{
		"ancestor::*[1]": true, "ancestor::*[@id][1]": true, "ancestor::*[position()=1]": true,
		"ancestor::*[1=position()]": true, "ancestor::*[@a][@b = 'x'][1]": true, "ancestor::*[q:c[2]][1]": true,
		"ancestor::*[name()][1]": true, "ancestor::*[2]": false, "ancestor::*[last()]": false,
		"ancestor::*[position() < 3][1]": false, "ancestor::*[count(*)][1]": false, "ancestor::*[$v][1]": false,
		"ancestor::*[1][@id]": false, "ancestor::*[last() = 3 and @a][1]": false, "ancestor::*[-1]": false,
	}
	for src, want := range first {
		e, err := Compile(src, Options{Namespaces: indexNS, Variables: map[string]bool{"v": true}})
		if err != nil {
			t.Fatal(err)
		}
		if got := e.root.(*pathExpr).steps[0].first; got != want {
			t.Errorf("%s: first %v, want %v", src, got, want)
		}
	}
	keyed := map[string]bool{
		"//a[@id = current()/@ref]": true, "//a[current()/@ref = @id]": true, "//a[@id = $v]": true,
		"/r/a[@b][@id = concat($v, current())]": true, "//a[@id = //b/@ref]": false, // remembered instead
		"//a[@id = @ref]": false, "//a[@id = current()/@ref][1]": false, "//a[@id = name()]": false,
		"//a[@id = position()]": false, "//a[@* = $v]": false, "//a[string(@id) = $v]": false,
		"//a[@id != $v]": false, "//a[1][@id = $v]": true, "a[@id = $v]": false, "//a[@id = $v/b]": true,
		"//a[@id = current()/@ref or @x]": false, "//a[$v][@id = $v]": false,
	}
	for src, want := range keyed {
		e, err := Compile(src, Options{Variables: map[string]bool{"v": true}})
		if err != nil {
			t.Fatal(err)
		}
		p, _ := e.root.(*pathExpr)
		if got := p != nil && p.keyed != nil; got != want {
			t.Errorf("%s: keyed %v, want %v", src, got, want)
		}
	}
}

// A step that stops at its first node pays for the nodes up to it: the
// nearest matching ancestor of a node deep down costs a few steps, wherever
// the farther ones are.
func TestFirstStepStopsEarly(t *testing.T) {
	doc := nestedDoc(t, 3000, 1, 0)
	var leaf *Node // the c at the bottom
	for _, n := range elements(doc.Root) {
		if id, _ := plainAttribute(n, "id"); id == "c0" {
			leaf = n
		}
	}
	for _, src := range []string{"ancestor::q:i[1]", "ancestor::*[@id][1]", "ancestor::*[position() = 1]/@id"} {
		e, err := Compile(src, Options{Namespaces: indexNS})
		if err != nil {
			t.Fatal(err)
		}
		ns, err := e.Select(Context{Node: leaf, Session: NewSession(context.Background(), 20)})
		if err != nil || len(ns) != 1 {
			t.Errorf("%s: %v, %v", src, ns, err)
		}
	}
	// Without a match the whole axis is walked and paid for.
	e, err := Compile("ancestor::q:none[1]", Options{Namespaces: indexNS})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Evaluate(Context{Node: leaf, Session: NewSession(context.Background(), 3000)}); !errors.Is(err, ErrTooComplex) {
		t.Errorf("no match within 3000 steps: %v, want ErrTooComplex", err)
	}
}

// A rule that looks up, at each of n elements, the elements whose
// attribute it names takes steps in proportion to n, not n².
func TestKeyedPathsMakeLookupsLinear(t *testing.T) {
	const k = 3000
	doc := cubicDoc(t, k)
	for _, src := range []string{"//decl[@id = current()/@ref]", "count(//*[@ref = current()/@id]) = 1", "@ref = //decl/@id"} {
		e, err := Compile(src, Options{})
		if err != nil {
			t.Fatal(err)
		}
		session := NewSession(context.Background(), 30*k)
		for _, n := range elements(doc.Root) {
			if _, err := e.Evaluate(Context{Node: n, Session: session}); err != nil {
				t.Fatalf("%s: %v", src, err)
			}
		}
	}
}

// Paths into nested subtrees and steps that stop early keep nested
// documents linear: one evaluation from all the nested elements, and the
// nearest ancestor of each, take about twice the steps in a document twice
// as deep.
func TestNestedStepsScale(t *testing.T) {
	compile := func(src string) *Expr {
		e, err := Compile(src, Options{Namespaces: indexNS, Variables: map[string]bool{"all": true}})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	all, fromAll, nearest := compile("//q:i"), compile("count($all//q:c)"), compile("ancestor::q:i[1]")
	steps := func(depth int) int64 {
		doc := nestedDoc(t, depth, depth, 0)
		const budget = int64(1) << 40
		session := NewSession(context.Background(), budget)
		v, err := all.Evaluate(Context{Node: doc.Root, Session: session})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fromAll.Evaluate(Context{Node: doc.Root, Variables: map[string]Value{"all": v}, Session: session}); err != nil {
			t.Fatal(err)
		}
		for _, n := range elements(doc.Root) {
			if _, err := nearest.Evaluate(Context{Node: n, Session: session}); err != nil {
				t.Fatal(err)
			}
		}
		return budget - session.remaining
	}
	small, large := steps(500), steps(1000)
	if large > 3*small {
		t.Errorf("500 levels: %d steps, 1000 levels: %d", small, large)
	}
}
