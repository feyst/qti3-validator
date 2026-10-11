package xpath

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// idsDoc has n elements a with ids i0, i1, ... and n elements b that refer
// to them.
func idsDoc(t testing.TB, n int) *Document {
	t.Helper()
	var b strings.Builder
	b.WriteString("<r>")
	for i := range n {
		fmt.Fprintf(&b, `<a id="i%d"/><b ref="i%d">i%d</b>`, i, i, i)
	}
	b.WriteString("</r>")
	doc, err := Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// A string compared with a kept node-set is looked up in its string-values:
// a step, where comparing every node takes one each. The result is that of
// plain evaluation, and other comparisons are left as they were.
func TestStringIsLookedUpInKeptSet(t *testing.T) {
	const n = 2000
	doc := idsDoc(t, n)
	defer func() { optimise = true }()
	for src, want := range map[string]float64{
		"count(//b[string(@ref) = //a/@id])":                          n,
		"count(//b[//a/@id = string(@ref)])":                          n,
		"count(//b[concat(@ref, 'x') = //a/@id])":                     0,
		"count(//b[substring-before(concat(., ' '), ' ') = //a/@id])": n,
		"count(//b[string(@ref) != //a/@id])":                         n, // not looked up
		"count(//b[string-length(@ref) = //a/@id])":                   0, // a number
		"count(//b[true() = //a/@id])":                                n, // a boolean
	} {
		var steps [2]int64
		for i, on := range []bool{false, true} {
			optimise = on
			e, err := Compile(src, Options{})
			if err != nil {
				t.Fatal(err)
			}
			s := NewSession(context.Background(), testBudget)
			got, err := e.Evaluate(Context{Node: doc.Root, Session: s})
			if err != nil || got != want {
				t.Errorf("optimise %v, %s: got %v, %v; want %v", on, src, got, err, want)
			}
			steps[i] = stepsUsed(s)
		}
		if strings.Contains(src, " = //a/@id") && strings.Contains(src, "string(") && !strings.Contains(src, "length") && steps[1] > 20*n {
			t.Errorf("%s: %d steps with the lookup, %d without; want a few a node", src, steps[1], steps[0])
		}
	}
}

// A value that is scoped is looked up like a constant one until it is
// released; the session then holds nothing for it.
func TestScopedValues(t *testing.T) {
	const n = 2000
	doc := idsDoc(t, n)
	ids, err := Compile("//a/@id", Options{})
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Variables: map[string]bool{"ids": true}}
	e, err := Compile("count(//b[string(@ref) = $ids])", opts)
	if err != nil {
		t.Fatal(err)
	}
	// Evaluated without a session: not the remembered node-set of one.
	v, err := ids.Evaluate(Context{Node: doc.Root})
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]Value{"ids": v}
	s := NewSession(context.Background(), testBudget)
	run := func() int64 {
		t.Helper()
		before := stepsUsed(s)
		got, err := e.Evaluate(Context{Node: doc.Root, Variables: vars, Session: s})
		if err != nil || got != float64(n) {
			t.Fatalf("got %v, %v", got, err)
		}
		return stepsUsed(s) - before
	}
	plain := run()
	release := s.Scoped(v)
	scoped := run()
	if len(s.sets) != 1 {
		t.Errorf("%d sets kept while scoped, want 1", len(s.sets))
	}
	release()
	released := run()
	if len(s.sets) != 0 || s.setBytes != 0 {
		t.Errorf("%d sets and %d bytes kept after the release", len(s.sets), s.setBytes)
	}
	t.Logf("%d steps not scoped, %d scoped, %d released", plain, scoped, released)
	if scoped > 20*n || plain < n*n/4 || released < n*n/4 {
		t.Errorf("%d steps scoped, %d before and %d after", scoped, plain, released)
	}
	// A value the session keeps anyway is not dropped by a release, and
	// small values and other types need no release.
	s.Constant(v)
	s.Scoped(v)()
	if len(s.sets) != 1 {
		t.Error("the release of a scope dropped a constant")
	}
	s.Scoped("text")()
	s.Scoped(NodeSet{doc.Root})()
	// The bytes built for a scoped set are given back.
	elements, err := Compile("//b", Options{})
	if err != nil {
		t.Fatal(err)
	}
	bs, err := elements.Evaluate(Context{Node: doc.Root})
	if err != nil {
		t.Fatal(err)
	}
	s2 := NewSession(context.Background(), testBudget)
	release = s2.Scoped(bs)
	if _, ok, err := s2.stringSet(bs.(NodeSet)); !ok || err != nil {
		t.Fatalf("the set of a scoped value: %v, %v", ok, err)
	}
	release()
	if s2.setBytes != 0 || len(s2.sets) != 0 {
		t.Errorf("%d bytes and %d sets after the release", s2.setBytes, len(s2.sets))
	}
}
