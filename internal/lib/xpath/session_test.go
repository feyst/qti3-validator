package xpath

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// elements lists the elements of a document in document order.
func elements(n *Node) []*Node {
	var out []*Node
	for _, c := range n.Children {
		if c.Kind == ElementNode {
			out = append(out, c)
			out = append(out, elements(c)...)
		}
	}
	return out
}

func sameValue(a, b Value) bool {
	an, aSet := a.(NodeSet)
	bn, bSet := b.(NodeSet)
	if aSet || bSet {
		return aSet && bSet && slices.Equal(an, bn)
	}
	if af, ok := a.(float64); ok && math.IsNaN(af) {
		bf, ok := b.(float64)
		return ok && math.IsNaN(bf)
	}
	return a == b
}

// Remembered paths give the same results as evaluating every expression on
// its own, at every context node, including expressions with current() and
// variables, which are not remembered.
func TestSessionGivesTheSameResults(t *testing.T) {
	doc := parseFile(t, "testdata/doc.xml")
	data, err := os.ReadFile("testdata/exprs.txt")
	if err != nil {
		t.Fatal(err)
	}
	sources := strings.Split(strings.TrimSpace(string(data)), "\n")
	sources = append(sources,
		"//d:item[@n = current()/@n]",
		"count(//*[name() = name(current())])",
		"//*[@n = $v]",
		"count(//d:item[@n > 1][. = //d:item[last()]])",
		"(//d:item)[2] | //d:item[1]",
		"//d:item[position() = last()]",
	)
	session := NewSession(context.Background(), 0)
	vars := map[string]Value{"v": "2"}
	for _, src := range sources {
		e, err := Compile(src, Options{Namespaces: testNS, Variables: map[string]bool{"v": true}})
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		for _, n := range elements(doc.Root) {
			want, wantErr := e.Evaluate(Context{Node: n, Variables: vars})
			got, gotErr := e.Evaluate(Context{Node: n, Variables: vars, Session: session})
			if (wantErr != nil) != (gotErr != nil) || !sameValue(got, want) {
				t.Fatalf("%s at %s: got %v, %v; want %v, %v", src, n.Path(), got, gotErr, want, wantErr)
			}
		}
	}
}

func TestIndependentPaths(t *testing.T) {
	for src, want := range map[string][]bool{
		"//a[@x = //b/@y]":                         {true, true},
		"//a[@x = current()/@y]":                   {false},
		"//a[@x = $v]":                             {false},
		"//a[@x = current()/@y][@z = //b/@y]":      {false, true},
		"count(/r/a[position() = last()])":         {true},
		"a[@x = //b/@y]":                           {true},
		"(//a)[1]":                                 {true},
		"//a[count(//b[@y = current()/@x]) > 0]/c": {false, false},
	} {
		e, err := Compile(src, Options{Variables: map[string]bool{"v": true}})
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		var got []bool
		var walk func(expr)
		walk = func(x expr) {
			if p, ok := x.(*pathExpr); ok && p.absolute {
				got = append(got, p.independent)
			}
			for _, sub := range subexpressions(x) {
				walk(sub)
			}
		}
		walk(e.root)
		if !slices.Equal(got, want) {
			t.Errorf("%s: got %v, want %v", src, got, want)
		}
	}
}

// cubicDoc has k declarations and k interactions that refer to none of
// them, the worst case of a rule that compares every pair.
func cubicDoc(t testing.TB, k int) *Document {
	t.Helper()
	var b strings.Builder
	b.WriteString("<r>")
	for i := range k {
		fmt.Fprintf(&b, `<decl id="d%d" card="single"/>`, i)
	}
	for i := range k {
		fmt.Fprintf(&b, `<ci ref="x%d" max="2"/>`, i)
	}
	b.WriteString("</r>")
	doc, err := Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// The shape of a 1EdTech rule with an absolute path in a predicate, run at
// every interaction. Without remembering the paths, this takes k³ steps.
const cubicRule = "count(//decl[@card='single'][@id = //ci[@max and not(@max=1)]/@ref]) = 0"

func TestRememberedPathsMakeRulesLinear(t *testing.T) {
	const k = 400
	doc := cubicDoc(t, k)
	e, err := Compile(cubicRule, Options{})
	if err != nil {
		t.Fatal(err)
	}
	session := NewSession(context.Background(), 4*k*k)
	for _, n := range elements(doc.Root)[k:] {
		ok, err := e.EvaluateBool(Context{Node: n, Session: session})
		if err != nil || !ok {
			t.Fatalf("got %v, %v", ok, err)
		}
	}
}

func TestSessionBudget(t *testing.T) {
	doc := cubicDoc(t, 200)
	// string() keeps the session from looking the path up in an index.
	e, err := Compile("count(//*[string(@id) = current()//@*])", Options{})
	if err != nil {
		t.Fatal(err)
	}
	session := NewSession(context.Background(), 10_000)
	var failed error
	for _, n := range elements(doc.Root) {
		if _, failed = e.Evaluate(Context{Node: n, Session: session}); failed != nil {
			break
		}
	}
	if !errors.Is(failed, ErrTooComplex) {
		t.Fatalf("got %v, want ErrTooComplex", failed)
	}
	// Once spent, the budget stays spent.
	if _, err := e.Evaluate(Context{Node: doc.Root, Session: session}); !errors.Is(err, ErrTooComplex) {
		t.Fatalf("after the budget ran out: %v", err)
	}
}

func TestSessionStopsWhenContextEnds(t *testing.T) {
	doc := cubicDoc(t, 2000)
	// 4000 × 4000 nodes visited: the following axis of every element.
	e, err := Compile("count(//*[count(following::*) >= 0])", Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = e.Evaluate(Context{Node: doc.Root, Session: NewSession(ctx, 0)})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want a deadline error", err)
	}
	if late := time.Since(start) - 20*time.Millisecond; late > 100*time.Millisecond {
		t.Fatalf("stopped %v after the deadline", late)
	}
}
