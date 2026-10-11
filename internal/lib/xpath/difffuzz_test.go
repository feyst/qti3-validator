package xpath

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
)

// A differential test of the shortcuts (see optimise): generated
// expressions, evaluated at nodes of generated documents with and without
// them, give the same values and fail on the same evaluations. The
// generator is seeded, so the test is the same on every run; FZ_SEEDS runs
// it over more seeds than the few of a normal run.

var fzValues = []string{"1", " 1", "1.0", "01", "a", "a ", "", "NaN", "2", "-0", "0", "b", "1 "}

// fzDoc generates a document with mixed namespaces, whitespace and numeric
// looking attribute values, deep chains and wide fan-outs.
func fzDoc(r *rand.Rand, n int) string {
	var b strings.Builder
	b.WriteString(`<r xmlns:p="urn:p" xmlns:q="urn:q" x="a">`)
	names := []string{"a", "b", "c", "q:a", "q:b", "p:a", "p:c", "a"}
	count := 0
	var gen func(level int)
	gen = func(level int) {
		width := r.IntN(5)
		if r.IntN(10) == 0 {
			width = 20 // wide
		}
		if level > 2 && r.IntN(4) == 0 {
			width = 1 // chain
		}
		for i := 0; i < width && count < n; i++ {
			count++
			name := names[r.IntN(len(names))]
			fmt.Fprintf(&b, "<%s", name)
			switch r.IntN(8) {
			case 0:
				b.WriteString(` xmlns="urn:q"`)
			case 1:
				b.WriteString(` xmlns=""`)
			}
			for _, at := range []string{"x", "y", "p:x", "q:y"} {
				if r.IntN(3) == 0 {
					fmt.Fprintf(&b, ` %s="%s"`, at, fzValues[r.IntN(len(fzValues))])
				}
			}
			b.WriteString(">")
			switch r.IntN(7) {
			case 0, 1:
				b.WriteString(fzValues[r.IntN(len(fzValues))])
			case 2:
				b.WriteString("<!--c-->")
			case 3:
				b.WriteString("<?pi d?>")
			}
			if level < 14 && r.IntN(3) != 0 {
				gen(level + 1)
			}
			if r.IntN(5) == 0 {
				b.WriteString(fzValues[r.IntN(len(fzValues))])
			}
			fmt.Fprintf(&b, "</%s>", name)
		}
	}
	for count < n {
		gen(1)
	}
	b.WriteString("</r>")
	return b.String()
}

type fzGen struct{ r *rand.Rand }

func (g fzGen) pick(xs ...string) string { return xs[g.r.IntN(len(xs))] }

func (g fzGen) nameTest() string {
	return g.pick("a", "b", "c", "q:a", "q:b", "p:a", "p:c", "*", "q:*", "p:*", "node()", "text()", "comment()", "a", "q:a", "*")
}

func (g fzGen) axis() string {
	return g.pick("", "", "", "descendant::", "descendant-or-self::", "ancestor::", "ancestor-or-self::",
		"following::", "preceding::", "following-sibling::", "preceding-sibling::", "parent::", "self::", "child::")
}

func (g fzGen) attr() string { return g.pick("@x", "@y", "@p:x", "@q:y", "@x", "@x") }

func (g fzGen) value() string {
	return g.pick("$s", "$ns", "$cns", "$big", "$num", "$one", "$bool", "$nan", "$empty", "current()/@x",
		"current()/@y", "current()", "//b/@y", "//@x", "/r/@x", "'1'", "' 1'", "'a'", "'a '", "''", "1", "1.0",
		"true()", "concat($s, '')", "string(current()/@x)", "$nested/@x", "position()", "last()", "name()", "@y",
		"current()//@x", "count(//b)", "number(current()/@x)", "id('a')", "(//a)[1]/@x", "$s2")
}

func (g fzGen) pred(d int) string {
	if d > 2 {
		return g.pick("1", "@x", "position()=1", "2", "last()")
	}
	switch g.r.IntN(30) {
	case 0, 1, 2:
		return "1"
	case 3:
		return "position()=1"
	case 4:
		return "1=position()"
	case 5:
		return g.pick("2", "last()", "position()<3", "last()-1", "position() mod 2 = 0", "1.5", "0", "-1")
	case 6:
		return g.pick("$num", "$one", "$s", "$bool", "$ns", "$nan", "$empty", "$cns")
	case 7:
		return g.pick("number(@x)", "string-length(@x)", "count(*)", "sum(@x)", "@x + 0", "-@x", "string-length()", "floor(1)")
	case 8:
		return g.pick(". = 1", "@x = 1", "@x = 1.0", "@x = 0", ". = 'a'", "@x != 1", "@x > 0", "number(.) = 1")
	case 9, 10, 11, 12, 13:
		return g.attr() + g.pick(" = ", " = ", " = ", " != ") + g.value()
	case 14:
		return g.value() + " = " + g.attr()
	case 15:
		return g.attr()
	case 16:
		return "not(" + g.attr() + ")"
	case 17:
		return g.relPath(d + 1)
	case 18:
		return g.pick("string(.)", "boolean(.)", "name()", "local-name()", "normalize-space()", "current()", "true()", "string(@x)", "concat(@x, '')")
	case 19:
		return g.attr() + " = " + g.value() + " and " + g.attr()
	case 20:
		return g.attr() + " = " + g.value() + " or " + g.pick("$s/x", "@y", "false()")
	case 21:
		return "self::" + g.nameTest()
	case 22:
		return g.relPath(d+1) + " = " + g.value()
	case 23:
		return g.attr() + " = " + g.absPath(d+1)
	case 24:
		return "count(" + g.relPath(d+1) + ") > 1"
	case 25:
		return "(" + g.relPath(d+1) + ")"
	case 26:
		return g.attr() + " = " + g.attr()
	case 27:
		return "lang('en')"
	case 28:
		return "position() = 1 and " + g.attr()
	}
	return g.attr() + " = $s"
}

func (g fzGen) step(d int) string {
	ax := g.axis()
	var s string
	switch g.r.IntN(12) {
	case 0:
		return g.pick("..", ".", "@*", g.attr(), "namespace::*")
	default:
		s = ax + g.nameTest()
	}
	np := 0
	switch g.r.IntN(4) {
	case 1, 2:
		np = 1
	case 3:
		np = 2 + g.r.IntN(2)
	}
	var preds strings.Builder
	for range np {
		preds.WriteString("[" + g.pred(d) + "]")
	}
	return s + preds.String()
}

func (g fzGen) relPath(d int) string {
	n := 1 + g.r.IntN(3)
	if d > 2 {
		n = 1
	}
	parts := []string{g.step(d)}
	for i := 1; i < n; i++ {
		parts = append(parts, g.pick("/", "//", "/"), g.step(d))
	}
	return strings.Join(parts, "")
}

func (g fzGen) absPath(d int) string {
	return g.pick("/", "//", "//", "//") + g.relPath(d)
}

func (g fzGen) pathExpr(d int) string {
	switch g.r.IntN(15) {
	case 12:
		return "(" + g.pick(".", "..", "ancestor::*", "//a", "$nested", "//*") + g.pick("|", "/@*|") + g.pick("@*", "../@*", "namespace::*", "//@x", "ancestor::*/@*", "$nested/namespace::*", "//text()") + ")/" + g.pick("descendant-or-self::node()", "descendant-or-self::*", "descendant::node()", "descendant-or-self::node()["+g.pred(d)+"]", "descendant-or-self::a", "/@*", "/.")
	case 13:
		return g.pick("//", "/r//", "/descendant::", "//*/") + g.nameTest() + g.pick("", "[@y]", "[1]", "[2]") + "[" + g.attr() + " = " + g.pick("name()", "local-name()", "string(.)", "position()", "last()", ".", "normalize-space()", "string()", "string(@y)", "current()", "name(current())", "string(current())", "lang('en')", "../@x", "$ns[1]", "$ns[position()=last()]", "(//@x)[2]", "translate(name(), 'q:', '')", "concat(name(), substring($s, 9))", "concat(local-name(), substring($s, 9))", "concat(position(), substring($s, 9))", "concat(last(), substring($s, 9))", "concat(., substring($s, 9))", "concat(normalize-space(), substring($s, 9))", "string(position() * $one)", "concat(@y, substring($s, 9))", "$ns[. = name()]", "concat(string(), substring(current()/@zz, 9))", "$ns[position() = 2]", "concat(string(lang('en')), $empty)", "string(number() * $one)") + "]"
	case 14:
		return g.pick("descendant::", "descendant-or-self::", ".//", "//", "$nested//", "..//") + g.pick("a", "q:a", "b", "p:a", "c") + g.pick("", "[1]", "[@x][1]", "[2]", "[last()]", "[@x = $s][position()=1]", "[$one]", "[@x]")
	case 0, 1, 2, 3:
		return g.absPath(d)
	case 4, 5:
		return g.relPath(d)
	case 6:
		return "(" + g.pathExpr(d+1) + ")[" + g.pred(d) + "]"
	case 7:
		return g.pick("$nested", "$cns", "$ns", "$big", "current()", "(.|@*)", "(ancestor::*|ancestor::*/@*)") + g.pick("//", "/") + g.relPath(d)
	case 8:
		return "(" + g.pathExpr(d+1) + " | " + g.pathExpr(d+1) + ")" + g.pick("//", "/") + g.relPath(d)
	case 9:
		return g.pathExpr(d+1) + " | " + g.pathExpr(d+1)
	case 10:
		return "(" + g.pathExpr(d+1) + ")" + g.pick("//", "/") + g.relPath(d)
	}
	return "../" + g.relPath(d)
}

func (g fzGen) expr() string {
	p := g.pathExpr(0)
	switch g.r.IntN(14) {
	case 0:
		return "count(" + p + ")"
	case 1:
		return p + g.pick(" = ", " != ") + g.pick("$big", "$cns", "$ns", "//@x", "//b", "$s", "$num", "$one", "$bool", "1", "'1'", "$nan", "$empty")
	case 2:
		return g.pick("$big", "$cns", "$ns", "//@x", "//@*") + g.pick(" = ", " != ") + p
	case 3:
		return "string(" + p + ")"
	case 4:
		return "sum(" + p + ")"
	case 5:
		return "boolean(" + p + ")"
	case 6:
		return p + " = " + g.pathExpr(1)
	case 7:
		return "number(" + p + ")"
	}
	return p
}

func fzContexts(r *rand.Rand, root *Node, k int) []*Node {
	var all []*Node
	var walk func(n *Node)
	walk = func(n *Node) {
		all = append(all, n)
		all = append(all, n.Attrs...)
		if n.Kind == ElementNode && r.IntN(20) == 0 {
			all = append(all, n.namespaces()...)
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(root)
	out := []*Node{root}
	for range k {
		out = append(out, all[r.IntN(len(all))])
	}
	return out
}

var fzVarSrc = map[string]string{
	"ns": "//@x", "big": "//@*", "cns": "//q:a", "nested": "//*[*]", "empty": "//nothing",
}

func fzVars(t *testing.T, doc *Document, s *Session) map[string]Value {
	v := map[string]Value{"s": "1", "s2": "a", "num": 2.0, "one": 1.0, "bool": true, "nan": math.NaN()}
	for name, src := range fzVarSrc {
		e, err := Compile(src, Options{Namespaces: fzNS})
		if err != nil {
			t.Fatal(err)
		}
		val, err := e.Evaluate(Context{Node: doc.Root, Session: s})
		if err != nil {
			t.Fatal(err)
		}
		v[name] = val
	}
	return v
}

var (
	fzNS       = map[string]string{"q": "urn:q", "p": "urn:p"}
	fzVarNames = map[string]bool{"s": true, "s2": true, "num": true, "one": true, "bool": true, "nan": true, "ns": true, "big": true, "cns": true, "nested": true, "empty": true}
)

func valString(v Value) string {
	if ns, ok := v.(NodeSet); ok {
		var b []string
		for _, n := range ns {
			b = append(b, n.Path()+"#"+strconv.Itoa(int(n.order)))
		}
		return fmt.Sprintf("nodes%v", b)
	}
	return fmt.Sprintf("%T %v", v, v)
}

func TestOptimisedMatchesPlainOnGeneratedExpressions(t *testing.T) {
	seeds, exprsPer, contexts := 3, 200, 10
	if slowdown > 1 {
		seeds, exprsPer, contexts = 2, 80, 5
	}
	if s := os.Getenv("FZ_SEEDS"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatal(err)
		}
		seeds, exprsPer, contexts = n, 400, 25
	}
	defer func() { optimise = true }()
	cases, skipped, reported := 0, 0, 0
	seen := map[string]bool{}
	for seed := uint64(1); seed <= uint64(seeds); seed++ {
		r := rand.New(rand.NewPCG(seed, 77))
		g := fzGen{r}
		var docs []*Document
		for range 2 {
			doc, err := Parse(strings.NewReader(fzDoc(r, 100+r.IntN(250))))
			if err != nil {
				t.Fatal(err)
			}
			docs = append(docs, doc)
		}
		type compiled struct {
			src         string
			plain, fast *Expr
		}
		var cs []compiled
		for range exprsPer {
			src := g.expr()
			optimise = false
			plain, err := Compile(src, Options{Namespaces: fzNS, Variables: fzVarNames})
			optimise = true
			if err != nil {
				continue // the generator may write what does not parse
			}
			fast, err := Compile(src, Options{Namespaces: fzNS, Variables: fzVarNames})
			if err != nil {
				t.Fatalf("%s compiles only without shortcuts: %v", src, err)
			}
			cs = append(cs, compiled{src, plain, fast})
		}
		// One session over both documents, like a session reused.
		session := NewSession(context.Background(), 0)
		constMask := r.IntN(32)
		for _, doc := range docs {
			fastVars := fzVars(t, doc, session)
			for i, name := range []string{"ns", "big", "cns", "nested", "empty"} {
				if constMask&(1<<i) != 0 {
					session.Constant(fastVars[name])
				}
			}
			optimise = false
			plainVars := fzVars(t, doc, nil)
			optimise = true
			ctxs := fzContexts(r, doc.Root, contexts)
			for _, c := range cs {
				for _, n := range ctxs {
					cases++
					optimise = false
					want, wantErr := c.plain.Evaluate(Context{Node: n, Variables: plainVars, Session: NewSession(context.Background(), 200_000)})
					optimise = true
					if errors.Is(wantErr, ErrTooComplex) {
						skipped++
						continue
					}
					got, gotErr := c.fast.Evaluate(Context{Node: n, Variables: fastVars, Session: session})
					if (wantErr != nil) == (gotErr != nil) && (wantErr != nil || sameValue(got, want)) {
						continue
					}
					if !seen[c.src] && reported < 20 {
						seen[c.src] = true
						reported++
						t.Errorf("seed %d, %s at %s: got %s, %v; want %s, %v", seed, c.src, n.Path(), valString(got), gotErr, valString(want), wantErr)
					}
				}
			}
		}
	}
	t.Logf("%d evaluations compared, %d skipped as too complex", cases, skipped)
}
