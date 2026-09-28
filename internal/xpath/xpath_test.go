package xpath

import (
	"encoding/json"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
)

var testNS = map[string]string{"d": "urn:default", "q": "urn:q", "x": "urn:x"}

func parseFile(t testing.TB, name string) *Document {
	t.Helper()
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	doc, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// libxml2Deviations are cases where libxml2 does not follow XPath 1.0; the
// value here is what the Recommendation prescribes.
var libxml2Deviations = map[string]string{
	// 4.2: numbers are never written with an exponent.
	"string(123456789012)": "123456789012",
	"string(0.000001)":     "0.000001",
	// 4.2: as many digits as needed to distinguish the number uniquely.
	"string(1 div 3)": "0.3333333333333333",
	// 4.4 with 3.7: the Number production has no exponent.
	"number('1e3')": "NaN",
	// Namespaces in XML 6.2: xmlns="" undeclares the default namespace, so
	// there is no namespace node for it. In scope: q, x and xml.
	"count(//nons/namespace::*)": "3",
}

// TestConformance compares results with libxml2. testdata/expected.json was
// produced by lxml 5 / libxml2 2.14.6 from testdata/exprs.txt on
// testdata/doc.xml, with the document element as context node.
func TestConformance(t *testing.T) {
	doc := parseFile(t, "testdata/doc.xml")
	context := doc.Root.Children[len(doc.Root.Children)-1] // after the leading PI
	data, err := os.ReadFile("testdata/expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ Expr, Type, Value string }
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		e, err := Compile(c.Expr, Options{Namespaces: testNS})
		if err != nil {
			t.Errorf("%s: %v", c.Expr, err)
			continue
		}
		v, err := e.Evaluate(Context{Node: context})
		if err != nil {
			t.Errorf("%s: %v", c.Expr, err)
			continue
		}
		if want, ok := libxml2Deviations[c.Expr]; ok {
			c.Value = want
		}
		if typeName(v) != c.Type {
			t.Errorf("%s: got %s %v, want %s %s", c.Expr, typeName(v), v, c.Type, c.Value)
			continue
		}
		var got string
		switch x := v.(type) {
		case NodeSet:
			got = strconv.Itoa(len(x))
		case float64:
			if !sameNumber(x, c.Value) {
				t.Errorf("%s: got %v, want %s", c.Expr, x, c.Value)
			}
			continue
		default:
			got = toString(v)
		}
		if got != c.Value {
			t.Errorf("%s: got %q, want %q", c.Expr, got, c.Value)
		}
	}
}

func sameNumber(got float64, want string) bool {
	switch want {
	case "NaN":
		return math.IsNaN(got)
	case "Infinity":
		return math.IsInf(got, 1)
	case "-Infinity":
		return math.IsInf(got, -1)
	case "-0":
		return got == 0 && math.Signbit(got)
	}
	w, err := strconv.ParseFloat(want, 64)
	return err == nil && w == got && math.Signbit(w) == math.Signbit(got)
}

func TestCompileErrors(t *testing.T) {
	for _, src := range []string{
		"", "1 +", "//", "foo(", "unknown()", "count()", "count(1, 2)", "p:x", "$undeclared",
		"document('x')", "key('a', 'b')", "'unterminated", "a!b", "child::", "bogus::x", "1 2",
		"//x[", "@", "concat('a')",
	} {
		if _, err := Compile(src, Options{Namespaces: testNS}); err == nil {
			t.Errorf("%q: want a compile error", src)
		}
	}
}

func TestDisambiguation(t *testing.T) {
	doc, err := Parse(strings.NewReader(`<r><div>3</div><mod>2</mod><and>1</and><or/></r>`))
	if err != nil {
		t.Fatal(err)
	}
	for src, want := range map[string]float64{
		"/r/div div /r/mod":   1.5,
		"/r/div mod /r/mod":   1,
		"count(/r/*)*2":       8,
		"count(//and | //or)": 2,
		"/r/div*2":            6,
		"count(/r/or)":        1,
	} {
		e, err := Compile(src, Options{})
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		v, err := e.Evaluate(Context{Node: doc.Root})
		if err != nil || v != want {
			t.Errorf("%s = %v, %v; want %v", src, v, err, want)
		}
	}
}

func TestVariablesAndCurrent(t *testing.T) {
	doc, err := Parse(strings.NewReader(`<r><a id="x"/><a id="y" ref="x"/><b>x</b></r>`))
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]bool{"ids": true, "n": true}
	refRule, err := Compile("//a[@id = current()/@ref]", Options{Variables: vars})
	if err != nil {
		t.Fatal(err)
	}
	ctx := doc.Root.Children[0].Children[1] // a[@id='y']
	ns, err := refRule.Select(Context{Node: ctx})
	if err != nil || len(ns) != 1 || ns[0].Attrs[0].Data != "x" {
		t.Fatalf("current(): got %v, %v", ns, err)
	}
	ids, _ := Compile("//a/@id", Options{})
	idSet, _ := ids.Select(Context{Node: doc.Root})
	e, err := Compile("count($ids[. = //b]) + $n", Options{Variables: vars})
	if err != nil {
		t.Fatal(err)
	}
	v, err := e.Evaluate(Context{Node: doc.Root, Variables: map[string]Value{"ids": idSet, "n": 10.0}})
	if err != nil || v != 11.0 {
		t.Fatalf("got %v, %v", v, err)
	}
}

func TestNameKeepsPrefix(t *testing.T) {
	doc, err := Parse(strings.NewReader(`<p:r xmlns:p="urn:p" xml:lang="nl" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:schemaLocation="a b"/>`))
	if err != nil {
		t.Fatal(err)
	}
	e, _ := Compile("concat(name(/*), '|', name(/*/@*[1]), '|', name(/*/@*[2]), '|', count(/*/@*))", Options{})
	got, err := e.EvaluateString(Context{Node: doc.Root})
	if err != nil || got != "p:r|xml:lang|xsi:schemaLocation|2" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestPositions(t *testing.T) {
	doc, err := Parse(strings.NewReader("<r>\n  <a/>\n  <b x='1'/>\n</r>"))
	if err != nil {
		t.Fatal(err)
	}
	e, _ := Compile("//b/@x", Options{})
	ns, _ := e.Select(Context{Node: doc.Root})
	if n := ns[0]; n.Line != 3 || n.Column != 3 || n.Path() != "/r[1]/b[1]/@x" {
		t.Fatalf("got line %d col %d path %s", n.Line, n.Column, n.Path())
	}
}

func TestConcurrentEvaluation(t *testing.T) {
	e, err := Compile("count(//d:item[@n > 1]) + string-length(name(/*))", Options{Namespaces: testNS})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			doc := parseFile(t, "testdata/doc.xml")
			for range 100 {
				if v, err := e.Evaluate(Context{Node: doc.Root}); err != nil || v != 8.0 {
					t.Errorf("got %v, %v", v, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
