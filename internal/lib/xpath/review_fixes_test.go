package xpath

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// used returns the steps an unlimited-enough session has taken.
const testBudget = int64(1) << 40

func stepsUsed(s *Session) int64 { return testBudget - s.remaining }

// nestedTextDoc has depth nested a elements, each starting with one
// character of its own, around text bytes of text, and one b with text y.
// The string-value of every a repeats the text at the bottom.
func nestedTextDoc(t testing.TB, depth, text int) *Document {
	t.Helper()
	var b strings.Builder
	b.WriteString("<r>")
	for i := range depth {
		b.WriteString("<a>")
		b.WriteByte(byte('A' + i%26))
	}
	b.WriteString(strings.Repeat("x", text))
	for range depth {
		b.WriteString("</a>")
	}
	b.WriteString("<b>y</b></r>")
	doc, err := Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// The set of string-values that = builds for a remembered node-set does
// not keep the text of nested elements once for each: it is bounded, and
// the comparison then goes node by node with the same result.
func TestStringSetMemoryIsBounded(t *testing.T) {
	const depth, text = 250, 2 << 20
	doc := nestedTextDoc(t, depth, text)
	retained := func(on bool, src string) (Value, int64, int64) {
		optimise = on
		defer func() { optimise = true }()
		e, err := Compile(src, Options{})
		if err != nil {
			t.Fatal(err)
		}
		s := NewSession(context.Background(), testBudget)
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		v, err := e.Evaluate(Context{Node: doc.Root, Session: s})
		if err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		if s.setBytes > maxSetBytes {
			t.Errorf("%s: the session keeps %d bytes of string-values, more than %d", src, s.setBytes, maxSetBytes)
		}
		runtime.KeepAlive(s)
		return v, int64(after.HeapAlloc) - int64(before.HeapAlloc), stepsUsed(s)
	}
	for _, src := range []string{"//b = //a", "//a = //b", "//a = //a"} {
		want, plainHeap, plainSteps := retained(false, src)
		got, heap, steps := retained(true, src)
		t.Logf("%s: plain %v, +%d MiB, %d steps; optimised %v, +%d MiB, %d steps",
			src, want, plainHeap>>20, plainSteps, got, heap>>20, steps)
		if got != want {
			t.Errorf("%s: got %v, want %v", src, got, want)
		}
		// Without the bound the session kept depth copies of the text.
		if limit := int64(2*maxSetBytes + text); heap > limit {
			t.Errorf("%s: the session retains %d MiB, more than %d MiB", src, heap>>20, limit>>20)
		}
	}
}

// A set that fits is still used, and what it builds is charged by its size.
func TestStringSetChargesBytes(t *testing.T) {
	var b strings.Builder
	b.WriteString("<r>")
	for i := range 100 {
		fmt.Fprintf(&b, "<a>%d<i/>%s</a>", i, strings.Repeat("x", 10*stringBytesPerStep))
	}
	b.WriteString("<b>y</b></r>")
	doc, err := Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	e, err := Compile("//b = //a", Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := NewSession(context.Background(), testBudget)
	if _, err := e.Evaluate(Context{Node: doc.Root, Session: s}); err != nil {
		t.Fatal(err)
	}
	if s.setBytes < 100*10*stringBytesPerStep {
		t.Fatalf("the set of //a was not built: %d bytes kept", s.setBytes)
	}
	if steps := stepsUsed(s); steps < 100*10 {
		t.Errorf("%d steps for a set of %d bytes, want at least %d", steps, s.setBytes, 100*10)
	}
}

// A step that ends in [1] does not stop at its first node when a predicate
// before it can raise a type error on a later node: the error is the result,
// as in plain evaluation.
func TestFirstStepKeepsErrors(t *testing.T) {
	doc, err := Parse(strings.NewReader(`<r><a x="1"/><a x="2"/></r>`))
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Variables: map[string]bool{"s": true, "n": true}}
	vars := map[string]Value{"s": "str", "n": NodeSet{doc.Root}}
	for _, src := range []string{
		"/r/a[@x='1' or $s/x][1]", "//a[@x='1' or $s/x][position()=1]", "/r/a[@x='1' or count($s)][1]",
		"/r/a[@x='1' or ($s | $n)][1]", "/r/a[@x='1' or $s[1]][1]", "/r/a[@x='1' or name($s) = 'r'][1]",
		"/r/a[@x='1' or b[$s/x]][1]",
		// No error: the variable is a node-set, or is not used as one.
		"/r/a[@x='1' or $n/x][1]", "/r/a[@x = $s][1]", "/r/a[@x='1' or string($s)][1]",
	} {
		optimise = false
		plain, err := Compile(src, opts)
		optimise = true
		if err != nil {
			t.Fatal(err)
		}
		fast, err := Compile(src, opts)
		if err != nil {
			t.Fatal(err)
		}
		optimise = false
		want, wantErr := plain.Evaluate(Context{Node: doc.Root, Variables: vars})
		optimise = true
		got, gotErr := fast.Evaluate(Context{Node: doc.Root, Variables: vars})
		if (wantErr != nil) != (gotErr != nil) || !sameValue(got, want) {
			t.Errorf("%s: got %v, %v; want %v, %v", src, got, gotErr, want, wantErr)
		}
	}
}

// Which predicates can raise a type error.
func TestMayFail(t *testing.T) {
	cases := map[string]bool{
		"@x": false, "@x = $v": false, "string($v)": false, "count(//a) > 1": false, "name(..) = 'a'": false,
		"a | b": false, "(//a)[1]": false, "current()/@x": false, "not(@x) or contains(@y, $v)": false,
		"$v/x": true, "$v | a": true, "$v[1]": true, "count($v) > 1": true, "name($v)": true, "sum($v)": true,
		"a[$v/x]": true, "@x or b[c[$v/x]]": true, "qtiv:count-by-attribute(., 'child', 'a', 'x', @x)": true,
		"generate-id($v)": true, "count('a')": true,
	}
	for src, want := range cases {
		e, err := Compile(src, Options{Namespaces: indexNS, Variables: map[string]bool{"v": true}})
		if err != nil {
			t.Fatal(err)
		}
		if got := mayFail(e.root); got != want {
			t.Errorf("%s: mayFail %v, want %v", src, got, want)
		}
	}
}

// A keyed lookup of several values sorts the nodes it found, and charges
// the comparisons of that sort.
func TestKeyedLookupChargesSort(t *testing.T) {
	const n = 4096
	var b strings.Builder
	b.WriteString("<r><k>k0</k><k>k1</k>")
	for i := range n {
		fmt.Fprintf(&b, `<a id="k%d"/>`, i%2)
	}
	b.WriteString("</r>")
	doc, err := Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	e, err := Compile("//a[@id = current()/k]", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if e.root.(*pathExpr).keyed == nil {
		t.Fatal("the path is not keyed")
	}
	s := NewSession(context.Background(), testBudget)
	ctx := Context{Node: doc.Root.Children[0], Session: s}
	if _, err := e.Evaluate(ctx); err != nil { // builds the index
		t.Fatal(err)
	}
	before := stepsUsed(s)
	v, err := e.Evaluate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(v.(NodeSet)); got != n {
		t.Fatalf("selected %d nodes, want %d", got, n)
	}
	steps := stepsUsed(s) - before
	t.Logf("a lookup of 2 values selecting %d nodes: %d steps", n, steps)
	if steps < n*12 { // log2(4096)
		t.Errorf("%d steps for sorting %d nodes, want at least %d", steps, n, n*12)
	}
}

// Session.Constant identifies a node-set by its first element in memory
// and its length, which is sound only while no evaluation changes a
// node-set in place. Whatever is done with a constant variable leaves its
// node-set as it was, and comparisons with it keep their results.
func TestConstantNodeSetsAreNotChanged(t *testing.T) {
	doc := randomDoc(t, 300, 6, 3)
	all, err := Compile("//*", Options{})
	if err != nil {
		t.Fatal(err)
	}
	v, err := all.Select(Context{Node: doc.Root})
	if err != nil {
		t.Fatal(err)
	}
	// Spare capacity, so that an append in place would be seen as well.
	const sentinelCount = 4
	sentinel := &Node{Kind: CommentNode, Data: "sentinel"}
	backing := make(NodeSet, len(v), len(v)+sentinelCount)
	copy(backing, v)
	for i := range sentinelCount {
		backing[:cap(backing)][len(v)+i] = sentinel
	}
	original := slices.Clone(backing[:cap(backing)])

	session := NewSession(context.Background(), 0)
	session.Constant(backing)
	opts := Options{Namespaces: indexNS, Variables: map[string]bool{"v": true}}
	vars := map[string]Value{"v": backing}
	probe, err := Compile("//@id = $v", opts)
	if err != nil {
		t.Fatal(err)
	}
	want, err := probe.Evaluate(Context{Node: doc.Root, Variables: vars, Session: session})
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{
		"$v[last()]", "$v[position() mod 2 = 0]", "($v)[1]", "$v[@id][1]", "$v | //@*", "//@* | $v", "$v | $v",
		"$v/..", "$v/ancestor::*", "$v/ancestor::*[1]", "$v/preceding::*[1]", "$v/preceding-sibling::*",
		"$v//*", "$v/descendant-or-self::node()", "$v/@*", "$v/self::*[1]", "($v/..)[last()]",
		"//*[. = $v]", "$v = $v", "$v != //@id", "count($v)", "sum($v)", "string($v)",
		"$v[. = //@id][1]", "(//@id | $v)[position() < 3]", "$v/following::*[last()]", "$v[not(*)]/parent::*",
	} {
		e, err := Compile(src, opts)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		for _, n := range []*Node{doc.Root, v[len(v)/2], v[len(v)-1]} {
			if _, err := e.Evaluate(Context{Node: n, Variables: vars, Session: session}); err != nil {
				t.Fatalf("%s: %v", src, err)
			}
			if !slices.Equal(backing[:cap(backing)], original) {
				t.Fatalf("%s changed the node-set of $v in place", src)
			}
		}
		got, err := probe.Evaluate(Context{Node: doc.Root, Variables: vars, Session: session})
		if err != nil || got != want {
			t.Fatalf("after %s: //@id = $v is %v, %v; want %v", src, got, err, want)
		}
	}
}

// A string of digits beyond the largest double is an infinity, the IEEE 754
// number nearest to it, not NaN.
func TestNumberBeyondRangeIsInfinity(t *testing.T) {
	huge := "1" + strings.Repeat("0", 330)
	for src, want := range map[string]float64{
		"number('" + huge + "')":        math.Inf(1),
		"number('-" + huge + "')":       math.Inf(-1),
		"number(' " + huge + ".5 ')":    math.Inf(1),
		huge:                            math.Inf(1),
		"-" + huge:                      math.Inf(-1),
		"number('0." + huge[1:] + "1')": 0, // too small: zero
		"number('1e400')":               math.NaN(),
		"number('Infinity')":            math.NaN(),
		"number('179769313486231570" + strings.Repeat("0", 291) + "')": math.MaxFloat64,
	} {
		e, err := Compile(src, Options{})
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		got, err := e.Evaluate(Context{Node: &Node{Kind: RootNode}})
		if err != nil {
			t.Fatal(err)
		}
		if !sameValue(got, want) {
			t.Errorf("%.40s...: got %v, want %v", src, got, want)
		}
	}
	e, err := Compile("string(number('"+huge+"'))", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := e.EvaluateString(Context{Node: &Node{Kind: RootNode}}); err != nil || got != "Infinity" {
		t.Errorf("got %q, %v; want Infinity", got, err)
	}
}

// Finding the document of a node does not take a step per ancestor: counts
// by attribute and descendant steps from the index cost the same however
// deep their context nodes lie.
func TestRootLookupDoesNotDependOnDepth(t *testing.T) {
	const k = 2000
	steps := func(depth int, src string) int64 {
		doc := nestedDoc(t, depth, k, 0)
		e, err := Compile(src, Options{Namespaces: indexNS})
		if err != nil {
			t.Fatal(err)
		}
		sel, err := Compile("//q:c[starts-with(@id, 'c')]", Options{Namespaces: indexNS})
		if err != nil {
			t.Fatal(err)
		}
		nodes, err := sel.Select(Context{Node: doc.Root})
		if err != nil || len(nodes) != k {
			t.Fatalf("selected %d nodes, %v", len(nodes), err)
		}
		s := NewSession(context.Background(), testBudget)
		for _, n := range nodes {
			v, err := e.Evaluate(Context{Node: n, Session: s})
			if err != nil {
				t.Fatal(err)
			}
			if v != 1.0 {
				t.Fatalf("%s at %s: got %v, want 1", src, n.Path(), v)
			}
		}
		return stepsUsed(s)
	}
	for _, src := range []string{
		"qtiv:count-by-attribute(.., 'child', '{urn:q}c', 'id', @id)",
		"qtiv:count-by-attribute(/, 'descendant', '{urn:q}c', 'id', @id)",
	} {
		shallow, deep := steps(2, src), steps(200, src)
		t.Logf("%s: %d steps at depth 2, %d at depth 200", src, shallow, deep)
		// The index of the deeper document has 600 more nodes to visit.
		if deep > shallow+10*200 {
			t.Errorf("%s: %d steps at depth 200, %d at depth 2", src, deep, shallow)
		}
	}
}

// Parse stores the root on every node, namespace nodes included.
func TestNodesKnowTheirRoot(t *testing.T) {
	doc, err := Parse(strings.NewReader(`<r xmlns:p="urn:p" a="1"><!--c--><?pi x?><p:b p:c="2">t</p:b></r>`))
	if err != nil {
		t.Fatal(err)
	}
	e, err := Compile("//node() | //@* | //namespace::* | /", Options{})
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := e.Select(Context{Node: doc.Root})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 12 {
		t.Fatalf("selected %d nodes, want 12", len(nodes))
	}
	for _, n := range nodes {
		if n.root != doc.Root || n.documentRoot() != doc.Root {
			t.Errorf("%s (kind %d): root is not the document's", n.Path(), n.Kind)
		}
	}
	// A node built by hand is followed up to its root.
	root := &Node{Kind: RootNode}
	el := &Node{Kind: ElementNode, Local: "a", Parent: root}
	if el.documentRoot() != root {
		t.Error("the root of a hand-built node is not found")
	}
}
