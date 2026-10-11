package xmldoc

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

const ns = "urn:example"

func TestDetectRoot(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want xml.Name
	}{
		{"default namespace", `<item xmlns="` + ns + `"/>`, xml.Name{Space: ns, Local: "item"}},
		{"prefixed", `<?xml version="1.0"?><!-- c --><?pi x?><q:test xmlns:q="` + ns + `"/>`, xml.Name{Space: ns, Local: "test"}},
		{"no namespace", `<item/>`, xml.Name{Local: "item"}},
		{"long document", `<item xmlns="` + ns + `">` + strings.Repeat("<p>x</p>", 20000) + `</item>`, xml.Name{Space: ns, Local: "item"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DetectRoot(strings.NewReader(tt.doc))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDetectRootErrors(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want error
	}{
		{"undeclared prefix", `<q:item/>`, nil},
		{"DTD", `<!DOCTYPE x><x/>`, ErrDTD},
		{"encoding", `<?xml version="1.0" encoding="UTF-16"?><x/>`, ErrUnsupportedEncoding},
		{"long prolog", `<!--` + strings.Repeat("x", MaxPrologBytes) + `--><x/>`, ErrPrologTooLong},
		{"empty", ``, nil},
		{"text only", `hello`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DetectRoot(strings.NewReader(tt.doc))
			if err == nil {
				t.Fatal("want an error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
}

func TestCheckWellFormed(t *testing.T) {
	if err := CheckWellFormed(t.Context(), strings.NewReader(`<a><b/></a>`), 0); err != nil {
		t.Fatal(err)
	}
	if err := CheckWellFormed(t.Context(), strings.NewReader(`<a><b></a>`), 0); err == nil {
		t.Fatal("accepted a mismatched end tag")
	}
	if err := CheckWellFormed(t.Context(), strings.NewReader(`<!DOCTYPE a><a/>`), 0); !errors.Is(err, ErrDTD) {
		t.Fatalf("got %v, want ErrDTD", err)
	}
}

// What CheckWellFormed rejects beyond the syntax errors of the decoder, and
// what it accepts.
func TestCheckWellFormedStructure(t *testing.T) {
	malformed := map[string]string{
		"two roots":                       `<a/><b/>`,
		"text after the root":             `<a/>junk`,
		"text before the root":            `junk<a/>`,
		"no root":                         `<!-- c -->`,
		"empty":                           ``,
		"unclosed":                        `<a><b>`,
		"end tag without start":           `<a/></a>`,
		"end tag with another prefix":     `<p:a xmlns:p="urn:p" xmlns:q="urn:p"></q:a>`,
		"duplicate attribute":             `<a x="1" x="2"/>`,
		"duplicate prefixed attribute":    `<a xmlns:p="urn:p" p:x="1" p:x="2"/>`,
		"duplicate by namespace":          `<a xmlns:p="urn:p" xmlns:q="urn:p" p:x="1" q:x="2"/>`,
		"duplicate by inherited prefixes": `<r xmlns:p="urn:p"><a xmlns:q="urn:p" p:x="1" q:x="2"/></r>`,
		"duplicate declaration":           `<a xmlns:p="urn:p" xmlns:p="urn:q"/>`,
		"duplicate default namespace":     `<a xmlns="urn:p" xmlns="urn:q"/>`,
		"undeclared element prefix":       `<a><p:b/></a>`,
		"undeclared attribute prefix":     `<a p:x="1"/>`,
		"prefix out of scope":             `<a><b xmlns:p="urn:p"/><p:c/></a>`,
		"declaration after the root":      `<a/><?xml version="1.0"?>`,
		"declaration after a comment":     `<!-- c --><?xml version="1.0"?><a/>`,
		"declaration after whitespace":    ` <?xml version="1.0"?><a/>`,
		"declaration inside the root":     `<a><?xml version="1.0"?></a>`,
		"two declarations":                `<?xml version="1.0"?><?xml version="1.0"?><a/>`,
		"character reference to NUL":      `<a>&#0;</a>`,
		"prefix undeclared":               `<r xmlns:p="urn:p"><a xmlns:p=""><p:b/></a></r>`,
		"prefix declared empty":           `<a xmlns:p=""/>`,
		"reserved target":                 `<a/><?XML version="1.0"?>`,
		"reserved target inside":          `<a><?Xml x?></a>`,
		"unknown entity":                  `<a>&nbsp;</a>`,
	}
	for name, doc := range malformed {
		err := CheckWellFormed(t.Context(), strings.NewReader(doc), 0)
		if err == nil {
			t.Errorf("%s: accepted %s", name, doc)
			continue
		}
		if _, ok := errors.AsType[*xml.SyntaxError](err); !ok {
			t.Errorf("%s: %v is not a syntax error", name, err)
		}
	}
	wellFormed := map[string]string{
		"declaration first":               `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<a/>` + "\n",
		"byte order mark":                 "\uFEFF" + `<?xml version="1.0"?><a/>`,
		"byte order mark, no declaration": "\uFEFF" + `<a/>`,
		"prolog and epilog":               `<!-- c --><?pi x?>` + "\n" + `<a/>` + "\n" + `<!-- c --><?pi y?>` + "\n",
		"other processing instruction":    `<a><?xml-stylesheet href="x"?><?xmlx y?></a>`,
		"namespaces":                      `<p:a xmlns:p="urn:p" xmlns="urn:d" p:x="1" x="1" xml:lang="en"><b xmlns:q="urn:q" q:x="1" p:x="2"><q:c/></b></p:a>`,
		"declaration after its use":       `<p:a p:x="1" xmlns:p="urn:p"/>`,
		"same local name":                 `<a xmlns:p="urn:p" xmlns:q="urn:q" x="1" p:x="2" q:x="3"/>`,
		"redeclared prefix":               `<a xmlns:p="urn:p" p:x="1"><b xmlns:p="urn:q" p:x="1"/></a>`,
		"text and references":             `<a>t &amp; &#65; <![CDATA[<x>]]></a>`,
		"undeclared default namespace":    `<a xmlns="urn:d"><b xmlns=""/></a>`,
		"US-ASCII":                        `<?xml version="1.0" encoding="US-ASCII"?><a/>`,
		"utf-8 in lower case":             `<?xml version='1.0' encoding='utf-8' standalone='yes'?><a/>`,
		"xml prefix":                      `<a xml:lang="en" xmlns:xml="http://www.w3.org/XML/1998/namespace"><b xml:space="preserve"/></a>`,
		"prefix restored":                 `<p:r xmlns:p="u1"><p:a xmlns:p="u2" xmlns:q="u1" p:x="1" q:x="1"><p:b/></p:a><p:c xmlns:q="u2" p:x="1" q:x="1"/></p:r>`,
		"prefix in sibling scopes":        `<r><p:a xmlns:p="u"/><q:a xmlns:q="u"/><a xmlns:p="v" p:x="1"/></r>`,
	}
	for name, doc := range wellFormed {
		if err := CheckWellFormed(t.Context(), strings.NewReader(doc), 0); err != nil {
			t.Errorf("%s: rejected %s: %v", name, doc, err)
		}
	}
	// The line of a violation is that of the token it is in.
	err := CheckWellFormed(t.Context(), strings.NewReader("<a>\n<b/>\n</a>\n<c/>"), 0)
	if syntax, ok := errors.AsType[*xml.SyntaxError](err); !ok || syntax.Line != 4 || !strings.Contains(syntax.Msg, "second root") {
		t.Errorf("got %v", err)
	}
	if err := CheckWellFormed(t.Context(), strings.NewReader(`<a/><!DOCTYPE a>`), 0); !errors.Is(err, ErrDTD) {
		t.Errorf("got %v, want ErrDTD", err)
	}
	// encoding/xml reads XML 1.0 only.
	if err := CheckWellFormed(t.Context(), strings.NewReader(`<?xml version="1.1"?><a/>`), 0); err == nil {
		t.Error("accepted XML 1.1")
	}
}

// A declared encoding that is not read is reported as such, as DetectRoot
// does, not as a syntax error.
func TestCheckWellFormedEncoding(t *testing.T) {
	for _, doc := range []string{
		`<?xml version="1.0" encoding="UTF-16"?><a/>`,
		`<?xml version="1.0" encoding="ISO-8859-1"?><a/>`,
	} {
		if err := CheckWellFormed(t.Context(), strings.NewReader(doc), 0); !errors.Is(err, ErrUnsupportedEncoding) {
			t.Errorf("%s: got %v, want ErrUnsupportedEncoding", doc, err)
		}
	}
}

// A prefix goes out of scope with the element that declares it, and comes
// back as an outer element bound it.
func TestCheckWellFormedPrefixScopes(t *testing.T) {
	malformed := []string{
		`<r><a xmlns:p="u"><p:b/></a><p:c/></r>`,
		`<r><a xmlns:p="u" xmlns:q="u"/><b q:x="1"/></r>`,
		`<r xmlns:p="u"><a xmlns:p="v" xmlns:q="u"/><b p:x="1" q:x="2"/></r>`,
		// p is urn:1 again after the inner element: the same name as q:x.
		`<r xmlns:p="urn:1" xmlns:q="urn:1"><a xmlns:p="urn:2" p:x="1" q:x="2"/><b p:x="1" q:x="2"/></r>`,
		// Two declarations of one prefix on an element, undone in turn.
		`<r><a xmlns:p="u" xmlns:p="v"/></r>`,
	}
	for _, doc := range malformed {
		if err := CheckWellFormed(t.Context(), strings.NewReader(doc), 0); err == nil {
			t.Errorf("accepted %s", doc)
		}
	}
}

func TestCheckWellFormedDepth(t *testing.T) {
	nested := func(depth int) string {
		return strings.Repeat("<a>", depth) + strings.Repeat("</a>", depth)
	}
	if err := CheckWellFormed(t.Context(), strings.NewReader(nested(256)+"\n"), 256); err != nil {
		t.Errorf("depth 256 of 256: %v", err)
	}
	if err := CheckWellFormed(t.Context(), strings.NewReader("<r><a/><a><b/></a><a/></r>"), 3); err != nil {
		t.Errorf("siblings do not add depth: %v", err)
	}
	err := CheckWellFormed(t.Context(), strings.NewReader("<a>\n<a>\n<a>\n<a/></a></a></a>"), 3)
	deep, ok := errors.AsType[*DepthError](err)
	if !ok || deep.Max != 3 || deep.Line != 4 {
		t.Errorf("depth 4 of 3: got %v", err)
	}
	// The limit comes before the end of the document: what follows is not read.
	err = CheckWellFormed(t.Context(), strings.NewReader(nested(257)+"<unclosed>"), 256)
	if _, ok := errors.AsType[*DepthError](err); !ok {
		t.Errorf("depth 257 of 256: got %v", err)
	}
	if err := CheckWellFormed(t.Context(), strings.NewReader(nested(5000)), 0); err != nil {
		t.Errorf("no limit: %v", err)
	}
}

// cancelAfter cancels a context once n bytes were read through it.
type cancelAfter struct {
	r      io.Reader
	n      int
	cancel context.CancelFunc
}

func (c *cancelAfter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if c.n -= n; c.n <= 0 {
		c.cancel()
	}
	return n, err
}

// The check stops soon after its context is done, not at the end of the
// document.
func TestCheckWellFormedStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := CheckWellFormed(ctx, strings.NewReader("<a/>"), 0); !errors.Is(err, context.Canceled) {
		t.Errorf("a context that is done: got %v", err)
	}

	const elements = 200_000
	doc := "<r>" + strings.Repeat("<a/>", elements) + "</r><second-root/>"
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	r := &countingReader{r: &cancelAfter{r: strings.NewReader(doc), n: len(doc) / 10, cancel: cancel}}
	err := CheckWellFormed(ctx, r, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want the error of the context", err)
	}
	// The decoder reads ahead in blocks of 4 KiB, and the context is
	// looked at every ctxCheckInterval tokens.
	if limit := len(doc)/10 + 2*4096 + 4*2*ctxCheckInterval; r.n > limit {
		t.Errorf("read %d of %d bytes after the context was done at %d", r.n, len(doc), len(doc)/10)
	}
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// The work of the check is in proportion to the size of the document: a
// document nested twice as deep, with a prefix on every element and
// attribute, takes about twice as long, and so does an element with twice
// the attributes. Resolving a prefix by walking the open elements made the
// first quadratic: 160,000 levels took half a minute.
func TestCheckWellFormedScalesLinearly(t *testing.T) {
	deep := func(depth int) string {
		return `<p:r xmlns:p="u">` + strings.Repeat(`<p:a p:x="1">`, depth) + strings.Repeat("</p:a>", depth) + `</p:r>`
	}
	// Every level declares a prefix, and uses the one of the root.
	declaring := func(depth int) string {
		return `<p:r xmlns:p="u">` + strings.Repeat(`<p:a xmlns:q="v" q:x="1">`, depth) + strings.Repeat("</p:a>", depth) + `</p:r>`
	}
	wide := func(attrs int) string {
		var b strings.Builder
		b.WriteString(`<r xmlns:p="u"`)
		for i := range attrs {
			fmt.Fprintf(&b, ` p:a%d="1" a%d="1" xmlns:n%d="u%d"`, i, i, i, i)
		}
		b.WriteString("/>")
		return b.String()
	}
	for name, doc := range map[string]func(int) string{"deep": deep, "declaring": declaring, "wide": wide} {
		const n = 40_000 / slowdown // the race detector makes it slow, not quadratic
		small, large := doc(n), doc(4*n)
		best := func(doc string) time.Duration {
			least := time.Duration(1<<63 - 1)
			for range 3 {
				start := time.Now()
				if err := CheckWellFormed(t.Context(), strings.NewReader(doc), 0); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				least = min(least, time.Since(start))
			}
			return least
		}
		a, b := best(small), best(large)
		t.Logf("%s: %d in %v, %d in %v", name, n, a, 4*n, b)
		// Four times the size: four times the time when linear, sixteen
		// when quadratic.
		if b > 9*a+20*time.Millisecond {
			t.Errorf("%s: %d took %v and %d took %v: not linear", name, n, a, 4*n, b)
		}
		if b > 5*time.Second*slowdown {
			t.Errorf("%s: %d took %v", name, 4*n, b)
		}
	}
}
