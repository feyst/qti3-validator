// Package xmldoc inspects XML documents safely with the standard library:
// it finds the root element without reading the whole document, and checks
// well-formedness without loading external entities or expanding DTD-declared
// ones.
package xmldoc

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// MaxPrologBytes bounds how far the root element may be from the start of a
// document: the XML declaration, comments and processing instructions.
const MaxPrologBytes = 64 << 10

var (
	// ErrPrologTooLong reports a root element after MaxPrologBytes.
	ErrPrologTooLong = errors.New("no root element in the first 64 KiB")
	// ErrUnsupportedEncoding reports a declared encoding other than UTF-8.
	ErrUnsupportedEncoding = errors.New("only UTF-8 is supported")
	// ErrDTD reports a DOCTYPE declaration.
	ErrDTD = errors.New("DOCTYPE declarations are not allowed")
)

// DetectRoot reads up to the root start tag and returns its name.
func DetectRoot(r io.Reader) (xml.Name, error) {
	lim := &prologReader{r: r, remaining: MaxPrologBytes}
	dec := xml.NewDecoder(lim)
	dec.Strict = true
	var charsetErr error
	dec.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		switch strings.ToLower(charset) {
		case "utf-8", "us-ascii":
			return input, nil
		}
		// encoding/xml formats this error with %v, so keep it for errors.Is.
		charsetErr = fmt.Errorf("%w, not %s", ErrUnsupportedEncoding, charset)
		return nil, charsetErr
	}
	for {
		tok, err := dec.RawToken()
		if err != nil {
			switch {
			case charsetErr != nil:
				err = charsetErr
			case lim.overflow:
				err = ErrPrologTooLong
			case errors.Is(err, io.EOF):
				err = errors.New("document has no root element")
			}
			return xml.Name{}, err
		}
		switch t := tok.(type) {
		case xml.Directive:
			return xml.Name{}, ErrDTD
		case xml.StartElement:
			return resolveRootName(t)
		}
	}
}

// resolveRootName resolves the namespace of the root element from its own
// declarations; RawToken leaves prefixes unresolved, and for the root only
// its own attributes can bind them.
func resolveRootName(t xml.StartElement) (xml.Name, error) {
	want := "xmlns"
	if t.Name.Space != "" {
		want = t.Name.Space
	}
	for _, a := range t.Attr {
		if t.Name.Space == "" && a.Name.Space == "" && a.Name.Local == "xmlns" ||
			t.Name.Space != "" && a.Name.Space == "xmlns" && a.Name.Local == want {
			return xml.Name{Space: a.Value, Local: t.Name.Local}, nil
		}
	}
	if t.Name.Space != "" {
		return xml.Name{}, fmt.Errorf("undeclared namespace prefix %q", t.Name.Space)
	}
	return xml.Name{Local: t.Name.Local}, nil
}

// prologReader stops after a number of bytes and remembers that it did.
type prologReader struct {
	r         io.Reader
	remaining int
	overflow  bool
}

func (p *prologReader) Read(b []byte) (int, error) {
	if p.remaining <= 0 {
		p.overflow = true
		return 0, ErrPrologTooLong
	}
	if len(b) > p.remaining {
		b = b[:p.remaining]
	}
	n, err := p.r.Read(b)
	p.remaining -= n
	return n, err
}

// CheckWellFormed reads a whole document and fails on the first thing that
// keeps it from being well-formed XML with well-formed namespaces, as far
// as it checks:
//
//   - a syntax error of encoding/xml in strict mode, such as a tag that is
//     not closed, a mismatched end tag, an unknown entity or a character
//     that XML does not allow;
//   - no root element, a second root element, or text outside the root;
//   - an XML declaration that is not at the very start (a byte order mark
//     may precede it);
//   - an attribute that occurs twice on an element, by its name as written
//     or by namespace and local name;
//   - a prefix of an element or an attribute that no xmlns attribute in
//     scope declares;
//   - a DOCTYPE declaration, which fails with ErrDTD.
//
// It does not check everything a validating parser would: names are those
// encoding/xml accepts, and the values of xmlns attributes and of the
// version in the XML declaration are not looked at. No entity is loaded or
// expanded other than the five predefined ones and character references.
//
// A violation other than a DOCTYPE is an *xml.SyntaxError with its line.
func CheckWellFormed(r io.Reader) error {
	dec := xml.NewDecoder(r)
	dec.Strict = true
	c := wellFormedness{scopes: []map[string]string{{"xml": "http://www.w3.org/XML/1998/namespace"}}}
	for {
		line, _ := dec.InputPos()
		offset := dec.InputOffset()
		tok, err := dec.RawToken()
		if errors.Is(err, io.EOF) {
			return c.end(line)
		}
		if err != nil {
			return err
		}
		if err := c.token(tok, offset); err != nil {
			if msg, ok := errors.AsType[violation](err); ok {
				return &xml.SyntaxError{Msg: string(msg), Line: line}
			}
			return err
		}
	}
}

// violation is a well-formedness error that CheckWellFormed found itself.
type violation string

func (v violation) Error() string { return string(v) }

// wellFormedness is the state of CheckWellFormed between tokens.
type wellFormedness struct {
	open   []xml.Name          // the open elements, names as written
	scopes []map[string]string // the prefixes each open element declares, with their namespaces
	root   bool                // the root element has started
	// started: a token other than a leading byte order mark was read, after
	// which an XML declaration is misplaced.
	started bool
	attrs   map[attrName]bool // reused for each element
}

// attrName identifies an attribute of an element: a namespace declaration
// by its name as written, an attribute without prefix by its local name,
// and one with a prefix by its namespace and local name, so that two
// prefixes of one namespace give the same name.
type attrName struct {
	kind  attrKind
	space string
	local string
}

type attrKind uint8

const (
	plainAttr attrKind = iota
	prefixedAttr
	declarationAttr
)

func (c *wellFormedness) token(tok xml.Token, offset int64) error {
	started := c.started
	c.started = true
	switch t := tok.(type) {
	case xml.Directive:
		return ErrDTD
	case xml.ProcInst:
		if t.Target == "xml" && started {
			return violation("XML declaration is not at the start of the document")
		}
	case xml.CharData:
		if len(c.open) > 0 {
			return nil
		}
		text := []byte(t)
		if offset == 0 && bytes.HasPrefix(text, []byte(byteOrderMark)) {
			text = text[len(byteOrderMark):]
			c.started = len(text) > 0
		}
		if len(bytes.TrimLeft(text, " \t\r\n")) > 0 {
			return violation("text outside the root element")
		}
	case xml.StartElement:
		return c.start(t)
	case xml.EndElement:
		last := len(c.open) - 1
		if last < 0 || c.open[last] != t.Name {
			return violation("unexpected end element </" + qualified(t.Name) + ">")
		}
		c.open, c.scopes = c.open[:last], c.scopes[:len(c.scopes)-1]
	}
	return nil
}

const byteOrderMark = "\uFEFF"

func (c *wellFormedness) start(t xml.StartElement) error {
	if len(c.open) == 0 {
		if c.root {
			return violation("second root element <" + qualified(t.Name) + ">")
		}
		c.root = true
	}
	// The declarations of the element are in scope for its own name and
	// for all its attributes, wherever among them they stand.
	var declared map[string]string
	for _, a := range t.Attr {
		if a.Name.Space == "xmlns" {
			if declared == nil {
				declared = map[string]string{}
			}
			declared[a.Name.Local] = a.Value
		}
	}
	c.open, c.scopes = append(c.open, t.Name), append(c.scopes, declared)
	if _, ok := c.resolve(t.Name.Space); !ok {
		return violation("undeclared namespace prefix " + strconv.Quote(t.Name.Space))
	}
	if c.attrs == nil {
		c.attrs = map[attrName]bool{}
	}
	clear(c.attrs)
	for _, a := range t.Attr {
		name := attrName{local: a.Name.Local}
		switch {
		case a.Name.Space == "xmlns", a.Name.Space == "" && a.Name.Local == "xmlns":
			name = attrName{kind: declarationAttr, local: qualified(a.Name)}
		case a.Name.Space != "":
			uri, ok := c.resolve(a.Name.Space)
			if !ok {
				return violation("undeclared namespace prefix " + strconv.Quote(a.Name.Space))
			}
			name = attrName{kind: prefixedAttr, space: uri, local: a.Name.Local}
		}
		if c.attrs[name] {
			return violation("attribute " + qualified(a.Name) + " occurs twice")
		}
		c.attrs[name] = true
	}
	return nil
}

// resolve returns the namespace that the open elements bind the prefix to.
// No prefix needs no declaration, and xml is always declared.
func (c *wellFormedness) resolve(prefix string) (string, bool) {
	if prefix == "" {
		return "", true
	}
	for i := len(c.scopes) - 1; i >= 0; i-- {
		if uri, ok := c.scopes[i][prefix]; ok {
			return uri, true
		}
	}
	return "", false
}

func (c *wellFormedness) end(line int) error {
	switch {
	case len(c.open) > 0:
		return &xml.SyntaxError{Msg: "unexpected end of document: <" + qualified(c.open[len(c.open)-1]) + "> is not closed", Line: line}
	case !c.root:
		return &xml.SyntaxError{Msg: "document has no root element", Line: line}
	}
	return nil
}

// qualified returns a name as written, which RawToken keeps as prefix and
// local name.
func qualified(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}
	return n.Space + ":" + n.Local
}

// Attr returns the value of an unqualified attribute of t.
func Attr(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if a.Name.Space == "" && a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}
