package xpath

import (
	"context"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"
)

// allNodes returns every node of a document in document order, namespace
// nodes of some elements included.
func allNodes(doc *Document) []*Node {
	var out []*Node
	var walk func(n *Node)
	walk = func(n *Node) {
		out = append(out, n)
		out = append(out, n.Attrs...)
		if n.Kind == ElementNode && len(out)%7 == 0 {
			out = append(out, n.namespaces()...)
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(doc.Root)
	return out
}

// Paths gives the path Node.Path gives, in whatever order the nodes are
// asked for, and again when asked twice.
func TestPathsMatchNodePath(t *testing.T) {
	for seed := uint64(1); seed <= 3; seed++ {
		nodes := allNodes(randomDoc(t, 400, 7, seed))
		orders := map[string][]*Node{"document order": nodes, "reverse": slices.Clone(nodes), "shuffled": slices.Clone(nodes), "every third": nil}
		slices.Reverse(orders["reverse"])
		r := rand.New(rand.NewPCG(seed, 9))
		r.Shuffle(len(nodes), func(i, j int) {
			orders["shuffled"][i], orders["shuffled"][j] = orders["shuffled"][j], orders["shuffled"][i]
		})
		for i := 0; i < len(nodes); i += 3 {
			orders["every third"] = append(orders["every third"], nodes[i])
		}
		for name, order := range orders {
			var p Paths
			for range 2 {
				for _, n := range order {
					if got, want := p.Path(n), n.Path(); got != want {
						t.Fatalf("seed %d, %s: got %s, want %s", seed, name, got, want)
					}
				}
			}
		}
	}
}

// The paths of many siblings take one pass over them together: four times
// the siblings take about four times as long, where Node.Path counts the
// siblings before each anew.
func TestPathsOfSiblingsScaleLinearly(t *testing.T) {
	doc := func(n int) *Document {
		d, err := Parse(strings.NewReader("<r>" + strings.Repeat("<a/><b/>text", n) + "</r>"))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	const n = 20_000 / slowdown
	best := func(d *Document) time.Duration {
		least := time.Duration(1<<63 - 1)
		for range 3 {
			start := time.Now()
			var p Paths
			last := ""
			for _, c := range d.Root.Children[0].Children {
				last = p.Path(c)
			}
			least = min(least, time.Since(start))
			if want := d.Root.Children[0].Children[len(d.Root.Children[0].Children)-1].Path(); last != want {
				t.Fatalf("got %s, want %s", last, want)
			}
		}
		return least
	}
	a, b := best(doc(n)), best(doc(4*n))
	t.Logf("%d siblings in %v, %d in %v", 3*n, a, 12*n, b)
	if b > 9*a+20*time.Millisecond {
		t.Errorf("%d siblings took %v and %d took %v: not linear", 3*n, a, 12*n, b)
	}
}

// Joining a string-value visits every node below the element, whatever
// text it finds: a session charges for the nodes as well as for the bytes.
func TestStringValueChargesNodesVisited(t *testing.T) {
	steps := func(n int) int64 {
		doc, err := Parse(strings.NewReader("<r><big>" + strings.Repeat("<a/>", n) + "</big><c/></r>"))
		if err != nil {
			t.Fatal(err)
		}
		e, err := Compile("string(/r/big) = ''", Options{})
		if err != nil {
			t.Fatal(err)
		}
		s := NewSession(context.Background(), testBudget)
		if v, err := e.Evaluate(Context{Node: doc.Root, Session: s}); err != nil || v != true {
			t.Fatalf("got %v, %v", v, err)
		}
		return stepsUsed(s)
	}
	const n = 64_000
	small, large := steps(n), steps(2*n)
	if grown, want := large-small, int64(n/stringNodesPerStep); grown != want {
		t.Errorf("%d more nodes cost %d steps more, want %d", n, grown, want)
	}
	if small < n/stringNodesPerStep {
		t.Errorf("%d steps for a string-value over %d nodes, want at least %d", small, n, n/stringNodesPerStep)
	}
	// An evaluation that asks for it at many nodes runs out of steps.
	doc, err := Parse(strings.NewReader("<r><big>" + strings.Repeat("<a/>", n) + "</big>" + strings.Repeat("<c/>", 2000) + "</r>"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := Compile("count(//c[string(/r/big) = ''])", Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := NewSession(context.Background(), 200_000) // three steps a node
	if _, err := e.Evaluate(Context{Node: doc.Root, Session: s}); err == nil {
		t.Error("2,000 string-values over 64,000 nodes each fit in 200,000 steps")
	}
}
