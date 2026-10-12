package xpath

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// contentBytes sums what Measure reports as TextBytes and ValueBytes, from
// the parsed document.
func contentBytes(n *Node) (text, value int64) {
	for _, a := range n.Attrs {
		value += int64(len(a.Data))
	}
	for _, c := range n.Children {
		if c.Kind != ElementNode {
			text += int64(len(c.Data))
			continue
		}
		t, v := contentBytes(c)
		text, value = text+t, value+v
	}
	return text, value
}

func checkMeasure(t *testing.T, name string, data []byte) {
	t.Helper()
	doc, err := Parse(bytes.NewReader(data))
	if err != nil {
		return // Measure promises nothing for what Parse refuses
	}
	got := Measure(data)
	if got.Nodes() != doc.Nodes {
		t.Errorf("%s: Measure counts %d nodes (%+v), Parse builds %d", name, got.Nodes(), got, doc.Nodes)
	}
	text, value := contentBytes(doc.Root)
	if got.TextBytes < text || got.ValueBytes < value {
		t.Errorf("%s: Measure counts %d text and %d value bytes, the document holds %d and %d",
			name, got.TextBytes, got.ValueBytes, text, value)
	}
}

// Measure counts the nodes Parse builds, kind by kind, without parsing.
func TestMeasureCountsWhatParseBuilds(t *testing.T) {
	for name, doc := range map[string]string{
		"empty element":      `<a/>`,
		"declaration":        `<?xml version="1.0" encoding="UTF-8"?><a/>`,
		"text":               `<a>x</a>`,
		"whitespace":         "<a>\n  <b/>\n  <b/>\n</a>\n",
		"outside the root":   "<!-- c -->\n<?pi x?>\n<a/>\n<!-- d -->\n",
		"attributes":         `<a x="1" y='2' z = "a>b"><b x="/>"/></a>`,
		"namespaces":         `<a xmlns="urn:a" xmlns:b="urn:b" b:x="1"><b:c xmlns:d='urn:d'/></a>`,
		"entities":           `<a x="&lt;&amp;">&lt;b&gt; &#65;</a>`,
		"cdata":              `<a><![CDATA[<b/>]]></a>`,
		"empty cdata":        `<a><![CDATA[]]></a>`,
		"cdata beside text":  `<a>x<![CDATA[y]]>z<![CDATA[]]><b/><![CDATA[q]]></a>`,
		"comment":            `<a>x<!-- <b/> -->y<!----></a>`,
		"instruction":        `<a><?pi a="b" > ?><?xml-stylesheet x?>t</a>`,
		"text after element": `<a><b/>x<b></b>y<b>z</b></a>`,
		"spaces in tags":     "<a\n x = '1'\t>\n<b  /></a >",
	} {
		checkMeasure(t, name, []byte(doc))
	}
	// The XML files of the repository's test data.
	root := filepath.Join("..", "..", "..", "testdata")
	files := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".xml") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files++
		checkMeasure(t, path, data)
		return nil
	})
	if err != nil || files == 0 {
		t.Fatalf("read %d files of %s: %v", files, root, err)
	}
}

// Measure never fails, whatever it is given.
func FuzzMeasure(f *testing.F) {
	for _, seed := range []string{`<a x="1">t<!--c--><?p i?><![CDATA[d]]></a>`, `<a`, `<a x=`, `<!--`, `<![CDATA[`, `</`, `<a x='`, `<?`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		checkMeasure(t, "fuzz", data)
	})
}

// retained is the heap a document of Parse keeps.
func retained(t *testing.T, data []byte) (int64, *Document) {
	t.Helper()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	doc, err := Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	return int64(after.HeapAlloc) - int64(before.HeapAlloc), doc
}

// DOMBytes follows what Parse really takes: never less, so memory reserved
// by it suffices, and not much more, so it does not refuse what would fit.
// A change to Node that makes a document larger fails here.
func TestMeasureDOMBytes(t *testing.T) {
	const size = 1 << 20
	repeat := func(unit string) []byte {
		return []byte("<r>" + strings.Repeat(unit, size/len(unit)) + "</r>")
	}
	for name, data := range map[string][]byte{
		"empty elements":     repeat(`<p/>`),
		"elements and text":  repeat(`<b/>x`),
		"text in elements":   repeat(`<p>x</p>`),
		"nested":             repeat(strings.Repeat(`<d>`, 50) + strings.Repeat(`</d>`, 50)),
		"attributes":         repeat(`<p id="a" dir="ltr" class="c" data-a="1" data-b="2"/>`),
		"long names":         repeat(`<qti-response-declaration identifier="R1" cardinality="single" base-type="identifier"/>`),
		"prose":              repeat(`<p>The quick brown fox jumps over the lazy dog, and <em>then</em> rests a while.</p>`),
		"comments":           repeat(`<!---->`),
		"one text":           repeat(`A`),
		"namespaced":         repeat(`<m:mi xmlns:m="urn:m">x</m:mi>`),
		"indented":           repeat("\n  <p>\n    <b>x</b>\n  </p>"),
		"attributes, quoted": repeat(`<p a='' b=''/>`),
	} {
		t.Run(name, func(t *testing.T) {
			got, doc := retained(t, data)
			want := Measure(data).DOMBytes()
			t.Logf("%d nodes: Parse keeps %d bytes (%.0f a node), DOMBytes %d (%.2f×)",
				doc.Nodes, got, float64(got)/float64(doc.Nodes), want, float64(want)/float64(got))
			if want < got {
				t.Errorf("DOMBytes %d is less than the %d bytes Parse keeps", want, got)
			}
			if want > got*3/2 {
				t.Errorf("DOMBytes %d is more than 1.5 times the %d bytes Parse keeps", want, got)
			}
			runtime.KeepAlive(doc)
		})
	}
}

// namespaceNodes counts the namespace nodes of the elements below n, which
// builds them.
func namespaceNodes(n *Node) int64 {
	total := int64(len(n.namespaces()))
	for _, c := range n.Children {
		total += namespaceNodes(c)
	}
	return total
}

// Measure counts the namespace nodes a step on the namespace axis builds,
// and NamespaceAxisBytes covers what they take.
func TestMeasureNamespaceNodes(t *testing.T) {
	for name, tc := range map[string]struct {
		doc  string
		want int64
	}{
		"none declared":     {`<a><b/><b><c/></b></a>`, 4},
		"declared at root":  {`<a xmlns="urn:a" xmlns:x="urn:x"><b/><x:b><c/></x:b></a>`, 12},
		"declared below":    {`<a><b xmlns:x="urn:x"><c/></b><b/></a>`, 6},
		"declared on empty": {`<a><b xmlns:x="urn:x"/><b/></a>`, 4},
	} {
		doc, err := Parse(strings.NewReader(tc.doc))
		if err != nil {
			t.Fatal(err)
		}
		if got, built := Measure([]byte(tc.doc)).NamespaceNodes, namespaceNodes(doc.Root); got != tc.want || built != tc.want {
			t.Errorf("%s: Measure counts %d namespace nodes, the axis builds %d, want %d", name, got, built, tc.want)
		}
	}
	// A prefix declared again counts twice, which is more than is built.
	again := `<a xmlns:x="urn:x"><b xmlns:x="urn:y"/></a>`
	doc, _ := Parse(strings.NewReader(again))
	if got, built := Measure([]byte(again)).NamespaceNodes, namespaceNodes(doc.Root); got < built {
		t.Errorf("Measure counts %d namespace nodes, the axis builds %d", got, built)
	}

	data := []byte(`<r xmlns:a="urn:a" xmlns:b="urn:b" xmlns:c="urn:c" xmlns:d="urn:d">` + strings.Repeat(`<p><b/></p>`, 50000) + `</r>`)
	var before, after runtime.MemStats
	doc, err := Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.ReadMemStats(&before)
	built := namespaceNodes(doc.Root)
	runtime.GC()
	runtime.ReadMemStats(&after)
	took, estimate := int64(after.HeapAlloc)-int64(before.HeapAlloc), Measure(data).NamespaceAxisBytes()
	t.Logf("%d namespace nodes take %d bytes, NamespaceAxisBytes %d (%.2f×)", built, took, estimate, float64(estimate)/float64(took))
	if estimate < took || estimate > took*3/2 {
		t.Errorf("NamespaceAxisBytes %d for namespace nodes that take %d bytes", estimate, took)
	}
	runtime.KeepAlive(doc)
}

func TestUsesNamespaceAxis(t *testing.T) {
	for src, want := range map[string]bool{
		`namespace::*`:                        true,
		`//p[count(namespace::*) > 2]`:        true,
		`count(a/b[c = d/namespace::x]) > 0`:  true,
		`string(namespace-uri(.))`:            false,
		`//p[@a]/ancestor::*[name() = 'x']`:   false,
		`(a | b)[1]/c[namespace::*[. = 'u']]`: true,
	} {
		e, err := Compile(src, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if got := e.UsesNamespaceAxis(); got != want {
			t.Errorf("%s: UsesNamespaceAxis %v, want %v", src, got, want)
		}
	}
}

// A document of more nodes than a Document holds is refused with
// ErrTooManyNodes.
func TestParseTooManyNodes(t *testing.T) {
	old := maxNodes
	maxNodes = 5
	defer func() { maxNodes = old }()
	if _, err := Parse(strings.NewReader(`<a><b/><b/><b/><b/></a>`)); err != nil {
		t.Fatalf("5 nodes: %v", err)
	}
	if _, err := Parse(strings.NewReader(`<a><b/><b/><b/><b x="1"/></a>`)); !errors.Is(err, ErrTooManyNodes) {
		t.Fatalf("6 nodes: %v", err)
	}
}
