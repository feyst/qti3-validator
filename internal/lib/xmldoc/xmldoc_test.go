package xmldoc

import (
	"encoding/xml"
	"errors"
	"strings"
	"testing"
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
	if err := CheckWellFormed(strings.NewReader(`<a><b/></a>`)); err != nil {
		t.Fatal(err)
	}
	if err := CheckWellFormed(strings.NewReader(`<a><b></a>`)); err == nil {
		t.Fatal("accepted a mismatched end tag")
	}
	if err := CheckWellFormed(strings.NewReader(`<!DOCTYPE a><a/>`)); !errors.Is(err, ErrDTD) {
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
		"unknown entity":                  `<a>&nbsp;</a>`,
	}
	for name, doc := range malformed {
		err := CheckWellFormed(strings.NewReader(doc))
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
	}
	for name, doc := range wellFormed {
		if err := CheckWellFormed(strings.NewReader(doc)); err != nil {
			t.Errorf("%s: rejected %s: %v", name, doc, err)
		}
	}
	// The line of a violation is that of the token it is in.
	err := CheckWellFormed(strings.NewReader("<a>\n<b/>\n</a>\n<c/>"))
	if syntax, ok := errors.AsType[*xml.SyntaxError](err); !ok || syntax.Line != 4 || !strings.Contains(syntax.Msg, "second root") {
		t.Errorf("got %v", err)
	}
	if err := CheckWellFormed(strings.NewReader(`<a/><!DOCTYPE a>`)); !errors.Is(err, ErrDTD) {
		t.Errorf("got %v, want ErrDTD", err)
	}
}
