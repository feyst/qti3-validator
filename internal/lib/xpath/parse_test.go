package xpath

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"
)

// childData returns the kinds and data of the children of the document
// element, as "kind:data" each.
func childData(t *testing.T, src string) []string {
	t.Helper()
	doc, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	var out []string
	for _, c := range doc.Root.Children[0].Children {
		out = append(out, fmt.Sprintf("%d:%s", c.Kind, c.Data))
	}
	return out
}

// A run of character data and CDATA sections is one text node with all of
// its pieces, and Measure counts what Parse builds.
func TestParseJoinsTextRuns(t *testing.T) {
	text, comment := strconv.Itoa(int(TextNode)), strconv.Itoa(int(CommentNode))
	for src, want := range map[string][]string{
		`<r>a<![CDATA[b]]>&amp;<![CDATA[]]>c&#65;<![CDATA[<d>]]></r>`: {text + ":ab&cA<d>"},
		`<r><![CDATA[a]]><![CDATA[b]]><![CDATA[c]]></r>`:              {text + ":abc"},
		// There are no empty text nodes: an empty section adds nothing.
		`<r><![CDATA[]]></r>`:                         nil,
		`<r><![CDATA[]]><![CDATA[]]></r>`:             nil,
		`<r><![CDATA[]]>a<![CDATA[]]></r>`:            {text + ":a"},
		`<r><![CDATA[]]><!--x--><![CDATA[]]>b</r>`:    {comment + ":x", text + ":b"},
		`<r>a<!--x-->b<![CDATA[c]]></r>`:              {text + ":a", comment + ":x", text + ":bc"},
		`<r><![CDATA[a]]><?p i?><![CDATA[b]]>c</r>`:   {text + ":a", strconv.Itoa(int(ProcessingInstructionNode)) + ":i", text + ":bc"},
		`<r>a<e>b<![CDATA[c]]></e><![CDATA[d]]>e</r>`: {text + ":a", strconv.Itoa(int(ElementNode)) + ":", text + ":de"},
		`<r>plain</r>`:                                {text + ":plain"},
	} {
		got := childData(t, src)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s: got %q, want %q", src, got, want)
		}
		checkMeasure(t, src, []byte(src))
	}
	// The text of a nested element is its own run, and ends with it.
	doc, err := Parse(strings.NewReader(`<r>a<e>b<![CDATA[c]]></e><![CDATA[d]]>e</r>`))
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Root.StringValue(); got != "abcde" {
		t.Errorf("got %q, want abcde", got)
	}
	if got := doc.Root.Children[0].Children[1].Children[0].Data; got != "bc" {
		t.Errorf("the text of e: got %q, want bc", got)
	}
}

// A prefix goes out of scope with the element that declares it, and is
// bound again as an outer element bound it.
func TestParseNamespaceScopes(t *testing.T) {
	doc, err := Parse(strings.NewReader(
		`<p:r xmlns:p="u1" xmlns="d1"><p:a xmlns:p="u2" xmlns="" p:x="1"><b xmlns:q="u3" q:y="2"/><p:c/></p:a><p:d p:x="3"/><e/></p:r>`))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	var walk func(n *Node)
	walk = func(n *Node) {
		if n.Kind == ElementNode {
			got = append(got, "{"+n.Space+"}"+n.Local)
		}
		for _, a := range n.Attrs {
			got = append(got, "@{"+a.Space+"}"+a.Local)
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(doc.Root)
	want := "{u1}r {u2}a @{u2}x {}b @{u3}y {u2}c {u1}d @{u1}x {d1}e"
	if strings.Join(got, " ") != want {
		t.Errorf("got %s, want %s", strings.Join(got, " "), want)
	}
	for _, src := range []string{
		`<r><a xmlns:p="u"/><p:b/></r>`,
		`<r><a xmlns:p="u"><b/></a><c p:x="1"/></r>`,
		`<r><p:a xmlns:p="u" xmlns:p="v"/><p:b/></r>`,
	} {
		if _, err := Parse(strings.NewReader(src)); err == nil {
			t.Errorf("accepted %s", src)
		}
	}
}

// Parsing takes time in proportion to the size of the document, however
// its text is cut into CDATA sections and however deep prefixed names lie.
// Appending every section to the text so far made the first quadratic:
// 80,000 sections took six seconds.
func TestParseScalesLinearly(t *testing.T) {
	cdata := func(n int) string {
		return "<r>" + strings.Repeat("<![CDATA[aaaaaaaaaaaa]]>", n) + "</r>"
	}
	mixed := func(n int) string {
		return "<r>" + strings.Repeat("a&amp;<![CDATA[b]]>", n) + "</r>"
	}
	deep := func(n int) string {
		return `<p:r xmlns:p="u">` + strings.Repeat(`<p:a p:x="1">`, n) + strings.Repeat("</p:a>", n) + "</p:r>"
	}
	for name, doc := range map[string]func(int) string{"cdata": cdata, "mixed": mixed, "deep": deep} {
		const n = 40_000 / slowdown // the race detector makes it slow, not quadratic
		best := func(src string) time.Duration {
			least := time.Duration(1<<63 - 1)
			for range 3 {
				start := time.Now()
				if _, err := Parse(strings.NewReader(src)); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				least = min(least, time.Since(start))
			}
			return least
		}
		a, b := best(doc(n)), best(doc(4*n))
		t.Logf("%s: %d in %v, %d in %v", name, n, a, 4*n, b)
		// Four times the size: four times the time when linear, sixteen
		// when quadratic.
		if b > 9*a+20*time.Millisecond {
			t.Errorf("%s: %d took %v and %d took %v: not linear", name, n, a, 4*n, b)
		}
	}
}

// cancelAfter cancels a context once n bytes were read through it, and
// counts the bytes read.
type cancelAfter struct {
	r      io.Reader
	n      int
	read   int
	cancel context.CancelFunc
}

func (c *cancelAfter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += n
	if c.read >= c.n {
		c.cancel()
	}
	return n, err
}

// ParseContext stops soon after its context is done, not at the end of the
// document.
func TestParseStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ParseContext(ctx, strings.NewReader("<a/>")); !errors.Is(err, context.Canceled) {
		t.Errorf("a context that is done: got %v", err)
	}
	for name, unit := range map[string]string{"elements": "<a/>", "sections": "<![CDATA[a]]>"} {
		doc := "<r>" + strings.Repeat(unit, 200_000) + "</r>"
		ctx, cancel := context.WithCancel(t.Context())
		r := &cancelAfter{r: strings.NewReader(doc), n: len(doc) / 10, cancel: cancel}
		_, err := ParseContext(ctx, r)
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%s: got %v, want the error of the context", name, err)
		}
		// The decoder reads ahead in blocks of 4 KiB, and the context is
		// looked at every parseCheckInterval tokens, two for an element.
		if limit := len(doc)/10 + 2*4096 + len(unit)*parseCheckInterval; r.read > limit {
			t.Errorf("%s: read %d of %d bytes after the context was done at %d", name, r.read, len(doc), len(doc)/10)
		}
	}
}
