package xpath

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The string-value of an element is the text below it, whether it is the
// Data of its one text node or joined from several.
func TestStringValue(t *testing.T) {
	for src, want := range map[string]struct {
		value string
		built bool
	}{
		`<a/>`:                                {"", false},
		`<a>text</a>`:                         {"text", false},
		`<a><b><c>text</c></b></a>`:           {"text", false},
		`<a><b><c/></b></a>`:                  {"", false},
		`<a><!--c--></a>`:                     {"", false},
		`<a><b><?p i?></b></a>`:               {"", false},
		`<a>x<![CDATA[y]]>z</a>`:              {"xyz", false}, // one text node
		`<a>x<b>y</b></a>`:                    {"xy", true},
		`<a><b>x</b><b>y</b></a>`:             {"xy", true},
		`<a><b>x<!--c-->y</b></a>`:            {"xy", true},
		`<a>x<!--c--></a>`:                    {"x", true},
		`<a><b/><c><d>x</d><e>y</e>z</c></a>`: {"xyz", true},
	} {
		doc, err := Parse(strings.NewReader(src))
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range []*Node{doc.Root, doc.Root.Children[0]} {
			got, built := n.stringValue()
			if got != want.value || built != want.built || n.StringValue() != want.value {
				t.Errorf("%s (kind %d): got %q, built %v; want %q, %v", src, n.Kind, got, built, want.value, want.built)
			}
		}
	}
	doc, err := Parse(strings.NewReader(`<a x="v"><!--c--><?p i?>t</a>`))
	if err != nil {
		t.Fatal(err)
	}
	a := doc.Root.Children[0]
	for i, n := range []*Node{a.Attrs[0], a.Children[0], a.Children[1], a.Children[2]} {
		if got, built := n.stringValue(); got != []string{"v", "c", "i", "t"}[i] || built {
			t.Errorf("kind %d: got %q, built %v", n.Kind, got, built)
		}
	}
}

// piecesDoc has an element a whose text is size bytes in two text nodes, an
// element b with the same text in one, and an attribute with it.
func piecesDoc(t testing.TB, size int) *Document {
	t.Helper()
	half := strings.Repeat("7", size/2)
	doc, err := Parse(strings.NewReader(`<r><a id="k">` + half + `<i/>` + half + `</a><b x="` + half + half + `">` + half + half + `</b><c id="k"/></r>`))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// Wherever an evaluation takes the string-value of an element that has to
// be built, it is charged a step per stringBytesPerStep bytes: twice the
// text costs twice as much. A string-value that is memory of the document,
// an attribute or the one text node of an element, costs no more than
// before.
func TestBuiltStringValuesAreCharged(t *testing.T) {
	const size = 64 << 10
	steps := func(doc *Document, src string, str bool) int64 {
		t.Helper()
		e, err := Compile(src, Options{})
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		s := NewSession(context.Background(), testBudget)
		ctx := Context{Node: doc.Root.Children[0], Session: s}
		if str {
			_, err = e.EvaluateString(ctx)
		} else {
			_, err = e.Evaluate(ctx)
		}
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		return stepsUsed(s)
	}
	small, large := piecesDoc(t, size), piecesDoc(t, 2*size)
	const perValue = size / stringBytesPerStep
	for src, values := range map[string]int64{
		"a = 'x'": 1, "'x' != a": 1, "a = b": 1, "a < 1": 1, "a = 1": 1, "a = a": 2,
		"string(a)": 1, "string-length(a)": 1, "normalize-space(a)": 1, "contains(a, 'x')": 1,
		"starts-with('x', a)": 1, "concat(a, 'x', a)": 2, "substring(a, 1, 2)": 1, "substring-after(a, 'x')": 1,
		"translate(a, '7', '8')": 1, "number(a)": 1, "floor(a)": 1, "sum(a | b)": 1, "-a": 1, "a + 1": 1,
		"1 div a": 1, "a mod a": 2, "a[string-length() > 0]": 1, "a[normalize-space()]": 1, "a[. = 'x']": 1,
		"a[number() > 0]": 1, "a[string()]": 1, "//c[@id = /r/a]": 1, "//a[. = 'x']": 1, "boolean(a/@id = a)": 1,
	} {
		got, double := steps(small, src, false), steps(large, src, false)
		if got < values*perValue || got > values*perValue+60 {
			t.Errorf("%s: %d steps, want %d for the bytes built and a few more", src, got, values*perValue)
		}
		if grown := double - got; grown != values*perValue {
			t.Errorf("%s: twice the text costs %d steps more, want %d", src, grown, values*perValue)
		}
	}
	// The result of EvaluateString, as in the value-of of a message.
	if got := steps(small, "a", true); got < perValue {
		t.Errorf("EvaluateString(a): %d steps, want at least %d", got, perValue)
	}
	// Not built: no more steps for more text.
	for _, src := range []string{
		"b = 'x'", "b/@x = 'x'", "string-length(b)", "contains(b, 'x')", "b = b/@x", "sum(b)", "b[. = 'x']",
		"count(a)", "boolean(a)", "not(a)", "a", "a | b", "name(a)", "a[1]", "//c[@id = /r/a/@id]",
	} {
		if got, double := steps(small, src, false), steps(large, src, false); got != double || got > 60 {
			t.Errorf("%s: %d steps, and %d with twice the text; want the same few", src, got, double)
		}
	}
}

// Comparing elements that nest builds the text at the bottom once for each
// of them. That work now shows in the steps: a small budget stops it, with
// or without the shortcuts, and it costs in proportion to the bytes.
func TestNestedStringValuesRunOutOfSteps(t *testing.T) {
	const depth, text = 250, (2 << 20) / slowdown // less text under the race detector
	doc := nestedTextDoc(t, depth, text)
	defer func() { optimise = true }()
	for _, on := range []bool{false, true} {
		optimise = on
		for _, src := range []string{"//b = //a", "//a = 'x'", "count(//a[string-length() > 5])", "sum(//a)", "//a[contains(., 'q')]"} {
			e, err := Compile(src, Options{})
			if err != nil {
				t.Fatal(err)
			}
			// Enough for the nodes many times over, not for their text.
			s := NewSession(context.Background(), 100_000)
			if _, err := e.Evaluate(Context{Node: doc.Root, Session: s}); !errors.Is(err, ErrTooComplex) {
				t.Errorf("optimise %v, %s: got %v, want ErrTooComplex", on, src, err)
			}
			s = NewSession(context.Background(), testBudget)
			if _, err := e.Evaluate(Context{Node: doc.Root, Session: s}); err != nil {
				t.Fatalf("%s: %v", src, err)
			}
			// Every a but the innermost, whose text is one node, builds the text
			// at the bottom.
			if got, want := stepsUsed(s), int64((depth-1)*(text/stringBytesPerStep)); got < want || got > want+want/10 {
				t.Errorf("optimise %v, %s: %d steps, want about %d", on, src, got, want)
			}
		}
	}
}
