package xpath

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// indexNS binds the prefixes of the tests of qtiv:count-by-attribute.
var indexNS = map[string]string{"q": "urn:q", "x": "urn:x", "qtiv": ExtensionNamespace}

// nestedDoc has depth nested q:i elements, each starting with a q:c of
// identifier dN, and k q:c elements at the bottom with identifiers cN; the
// identifiers of the bottom ones repeat every dupEvery (0: never).
func nestedDoc(t testing.TB, depth, k, dupEvery int) *Document {
	t.Helper()
	var b strings.Builder
	b.WriteString(`<r xmlns="urn:q">`)
	for i := range depth {
		fmt.Fprintf(&b, `<i id="i%d"><c id="d%d"/>`, i, i%7)
	}
	for i := range k {
		if dupEvery > 0 {
			i %= dupEvery
		}
		fmt.Fprintf(&b, `<c id="c%d"/>`, i)
	}
	for range depth {
		b.WriteString(`<c id="d3"/></i>`)
	}
	b.WriteString(`</r>`)
	doc, err := Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// matchesXPath checks that each pair of expressions, the count and the
// call that stands for it, returns the same number at every node of doc.
func matchesXPath(t *testing.T, doc *Document, pairs [][2]string) {
	t.Helper()
	for _, pair := range pairs {
		plain, err := Compile(pair[0], Options{Namespaces: indexNS})
		if err != nil {
			t.Fatal(err)
		}
		indexed, err := Compile(pair[1], Options{Namespaces: indexNS})
		if err != nil {
			t.Fatal(err)
		}
		session := NewSession(context.Background(), 0)
		var walk func(n *Node)
		walk = func(n *Node) {
			ctx := Context{Node: n}
			want, err := plain.Evaluate(ctx)
			if err != nil {
				t.Fatal(err)
			}
			got, err := indexed.Evaluate(ctx)
			if err != nil {
				t.Fatal(err)
			}
			ctx.Session = session // one index for all the evaluations
			shared, err := indexed.Evaluate(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got != want || shared != want {
				t.Errorf("%s on %s: indexed %v and %v, XPath %v", pair[0], n.Path(), got, shared, want)
			}
			for _, c := range n.Children {
				walk(c)
			}
			for _, a := range n.Attrs {
				walk(a)
			}
		}
		walk(doc.Root)
	}
}

// qtiv:count-by-attribute must return what the count it stands for
// returns, for every node as context node, including scopes of several
// nodes whose subtrees overlap.
func TestCountByAttributeMatchesXPath(t *testing.T) {
	doc, err := Parse(strings.NewReader(`<r xmlns="urn:q" xmlns:x="urn:x">
		<g id="a"><a id="a"/><b id="a"><a id="a"/><a id="b"/><x:a id="a"/></b><a x:id="a"/><a/><a id=""/><a id=""/></g>
		<g id="b"><b id="b"><g><a id="b"/></g></b><a id="a"/><c id="a"/>text<!--c--></g>
	</r>`))
	if err != nil {
		t.Fatal(err)
	}
	matchesXPath(t, doc, [][2]string{
		{"count(..//q:*[self::q:a or self::q:b][@id = current()/@id])", "qtiv:count-by-attribute(.., 'descendant', '{urn:q}a {urn:q}b', 'id', current()/@id)"},
		{"count(../*[self::q:a or self::x:a][@id = current()/@id])", "qtiv:count-by-attribute(.., 'child', '{urn:q}a {urn:x}a', 'id', current()/@id)"},
		{"count(//*[self::q:a][@id = current()/@id])", "qtiv:count-by-attribute(/, 'descendant', '{urn:q}a', 'id', current()/@id)"},
		{"count(/*[self::q:r][@id = current()/@id])", "qtiv:count-by-attribute(/, 'child', '{urn:q}r', 'id', current()/@id)"},
		{"count(ancestor-or-self::*//*[self::q:a or self::b][@id = current()/@id])", "qtiv:count-by-attribute(ancestor-or-self::*, 'descendant', '{urn:q}a {}b', 'id', current()/@id)"},
		{"count(ancestor-or-self::*/*[self::q:a][@id = current()/@id])", "qtiv:count-by-attribute(ancestor-or-self::*, 'child', '{urn:q}a', 'id', current()/@id)"},
		{"count(//q:g//*[self::q:a or self::q:c][@id = current()/@id])", "qtiv:count-by-attribute(//q:g, 'descendant', '{urn:q}a {urn:q}c', 'id', current()/@id)"},
		{"count(//q:g//*[self::q:a][@id = current()/@x:id])", "qtiv:count-by-attribute(//q:g, 'descendant', '{urn:q}a', 'id', current()/@x:id)"},
		{"count(//q:none//*[self::q:a][@id = current()/@id])", "qtiv:count-by-attribute(//q:none, 'descendant', '{urn:q}a', 'id', current()/@id)"},
		{"count((.|@*|namespace::*|text())//*[self::q:a][@id = //@id])", "qtiv:count-by-attribute(.|@*|namespace::*|text(), 'descendant', '{urn:q}a', 'id', //@id)"},
		{"count((@*|..)/*[self::q:a or self::q:b][@id = //@id])", "qtiv:count-by-attribute(@*|.., 'child', '{urn:q}a {urn:q}b', 'id', //@id)"},
	})
}

// Deeply nested scopes: the nearest scope, all the scopes around a node at
// once, and scopes that are not nested.
func TestCountByAttributeMatchesXPathInNestedScopes(t *testing.T) {
	doc := nestedDoc(t, 40, 30, 11)
	matchesXPath(t, doc, [][2]string{
		{"count(ancestor::q:i[1]//*[self::q:c][@id = current()/@id])", "qtiv:count-by-attribute(ancestor::q:i[1], 'descendant', '{urn:q}c', 'id', current()/@id)"},
		{"count(ancestor::q:i//*[self::q:c][@id = current()/@id])", "qtiv:count-by-attribute(ancestor::q:i, 'descendant', '{urn:q}c', 'id', current()/@id)"},
		{"count(ancestor::q:i[position() mod 3 = 0]//*[self::q:c or self::q:i][@id = current()/@id])", "qtiv:count-by-attribute(ancestor::q:i[position() mod 3 = 0], 'descendant', '{urn:q}c {urn:q}i', 'id', current()/@id)"},
		{"count(ancestor::q:i/*[self::q:c][@id = current()/@id])", "qtiv:count-by-attribute(ancestor::q:i, 'child', '{urn:q}c', 'id', current()/@id)"},
		{"count(//q:i[@id = 'i5' or @id = 'i20']//*[self::q:c][@id = //q:c/@id])", "qtiv:count-by-attribute(//q:i[@id = 'i5' or @id = 'i20'], 'descendant', '{urn:q}c', 'id', //q:c/@id)"},
	})
}

// heapGrowth runs f and returns the growth of the live heap it causes.
func heapGrowth(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.GC()
	runtime.ReadMemStats(&after)
	if after.HeapAlloc < before.HeapAlloc {
		return 0
	}
	return after.HeapAlloc - before.HeapAlloc
}

// Counting in each of 200 nested scopes over 150,000 elements must not walk
// or index each scope on its own: that took 7 seconds and 1.7 GB.
func TestCountByAttributeNestedScopesScale(t *testing.T) {
	const depth, k = 200, 150000
	doc := nestedDoc(t, depth, k, 0)
	test, err := Compile(`qtiv:count-by-attribute(ancestor::q:i[1], 'descendant', '{urn:q}c', 'id', current()/@id)`, Options{Namespaces: indexNS})
	if err != nil {
		t.Fatal(err)
	}
	// The first q:c of each q:i, and the first of the bottom ones: each has
	// a scope of its own.
	var nodes []*Node
	for n := doc.Root.Children[0].Children[0]; ; n = n.Children[1] {
		nodes = append(nodes, n.Children[0])
		if n.Children[1].Local != "i" {
			nodes = append(nodes, n.Children[1])
			break
		}
	}
	if len(nodes) != depth+1 {
		t.Fatalf("%d context nodes", len(nodes))
	}
	session := NewSession(context.Background(), 0)
	var took time.Duration
	grown := heapGrowth(func() {
		start := time.Now()
		for _, n := range nodes {
			v, err := test.Evaluate(Context{Node: n, Session: session})
			if err != nil {
				t.Fatal(err)
			}
			if v.(float64) < 1 {
				t.Fatalf("%s: count %v", n.Path(), v)
			}
		}
		took = time.Since(start)
	})
	runtime.KeepAlive(doc)
	t.Logf("%d evaluations over %d elements: %v, heap grew %d MB", len(nodes), depth+k, took, grown>>20)
	if took > slowdown*time.Second {
		t.Errorf("took %v", took)
	}
	if grown > 100<<20 {
		t.Errorf("heap grew %d MB", grown>>20)
	}
}

// A scope of many nodes is counted from the same index on every call.
func TestCountByAttributeManyScopeNodesScale(t *testing.T) {
	const k = 10000
	var b strings.Builder
	b.WriteString(`<r xmlns="urn:q">`)
	for i := range k {
		fmt.Fprintf(&b, `<g><c id="c%d"/></g>`, i%(k/2))
	}
	b.WriteString(`</r>`)
	doc, err := Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	ctxs, err := Compile(`(//q:c)[position() mod 10 = 1]`, Options{Namespaces: indexNS})
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := ctxs.Select(Context{Node: doc.Root})
	if err != nil {
		t.Fatal(err)
	}
	session := NewSession(context.Background(), 0)
	start := time.Now()
	for _, src := range []string{
		`qtiv:count-by-attribute(//q:g, 'descendant', '{urn:q}c', 'id', current()/@id)`,
		`qtiv:count-by-attribute(//q:g, 'child', '{urn:q}c', 'id', current()/@id)`,
	} {
		e, err := Compile(src, Options{Namespaces: indexNS})
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range nodes {
			v, err := e.Evaluate(Context{Node: n, Session: session})
			if err != nil {
				t.Fatal(err)
			}
			if v != 2.0 {
				t.Fatalf("%s at %s: %v, want 2", src, n.Path(), v)
			}
		}
	}
	took := time.Since(start)
	t.Logf("%d evaluations with %d scope nodes: %v", 2*len(nodes), k, took)
	if took > slowdown*2*time.Second {
		t.Errorf("took %v", took)
	}
}

// Building the index and every lookup are charged to the session.
func TestCountByAttributeIsCharged(t *testing.T) {
	doc := nestedDoc(t, 20, 5000, 0)
	e, err := Compile(`qtiv:count-by-attribute(/, 'descendant', '{urn:q}c', 'id', current()/@id)`, Options{Namespaces: indexNS})
	if err != nil {
		t.Fatal(err)
	}
	n := doc.Root.Children[0].Children[0].Children[0] // a q:c
	if _, err := e.Evaluate(Context{Node: n, Session: NewSession(context.Background(), 1000)}); !errors.Is(err, ErrTooComplex) {
		t.Fatalf("building the index with a small budget: %v, want ErrTooComplex", err)
	}
	// Once built, each lookup costs a few steps.
	session := NewSession(context.Background(), 20000)
	if _, err := e.Evaluate(Context{Node: n, Session: session}); err != nil {
		t.Fatal(err)
	}
	left := session.remaining
	if _, err := e.Evaluate(Context{Node: n, Session: session}); err != nil {
		t.Fatal(err)
	}
	if cost := left - session.remaining; cost < 1 || cost > 10 {
		t.Fatalf("a lookup cost %d steps", cost)
	}
	// A scope of many nodes costs a step per node.
	many, err := Compile(`qtiv:count-by-attribute(//q:c, 'descendant', '{urn:q}c', 'id', current()/@id)`, Options{Namespaces: indexNS})
	if err != nil {
		t.Fatal(err)
	}
	var failed error
	for range 10 {
		if _, failed = many.Evaluate(Context{Node: n, Session: session}); failed != nil {
			break
		}
	}
	if !errors.Is(failed, ErrTooComplex) {
		t.Fatalf("many scope nodes: %v, want ErrTooComplex", failed)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Evaluate(Context{Node: n, Session: NewSession(ctx, 0)}); !errors.Is(err, context.Canceled) {
		t.Fatalf("with a cancelled context: %v", err)
	}
}

// Only a prefix bound to ExtensionNamespace calls the function.
func TestCountByAttributeNeedsItsNamespace(t *testing.T) {
	for _, tc := range []struct {
		src string
		ns  map[string]string
	}{
		{"qtiv:count-by-attribute(/, 'child', 'r', 'id', @id)", nil},
		{"qtiv:count-by-attribute(/, 'child', 'r', 'id', @id)", map[string]string{"qtiv": "urn:other"}},
		{"count-by-attribute(/, 'child', 'r', 'id', @id)", nil},
		{"e:count(/)", map[string]string{"e": ExtensionNamespace}},
		{"e:no-such-function(/)", map[string]string{"e": ExtensionNamespace}},
	} {
		if _, err := Compile(tc.src, Options{Namespaces: tc.ns}); err == nil {
			t.Errorf("%s with %v: compiles", tc.src, tc.ns)
		}
	}
	if _, err := Compile("e:count-by-attribute(/, 'child', 'r', 'id', @id)", Options{Namespaces: map[string]string{"e": ExtensionNamespace}}); err != nil {
		t.Error(err)
	}
}

func TestCountByAttributeErrors(t *testing.T) {
	doc, err := Parse(strings.NewReader(`<r id="a"/>`))
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{
		"qtiv:count-by-attribute('x', 'child', 'r', 'id', @id)",
		"qtiv:count-by-attribute(/, 'parent', 'r', 'id', @id)",
		"qtiv:count-by-attribute(/, 'child', 'r', 'id', 'a')",
	} {
		e, err := Compile(src, Options{Namespaces: indexNS})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Evaluate(Context{Node: doc.Root.Children[0]}); err == nil {
			t.Errorf("%s: want an error", src)
		}
	}
}
