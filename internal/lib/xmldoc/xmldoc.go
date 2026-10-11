// Package xmldoc inspects XML documents safely with the standard library:
// it finds the root element without reading the whole document, and checks
// well-formedness without loading external entities or expanding DTD-declared
// ones.
package xmldoc

import (
	"bytes"
	"context"
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
	var charset charsets
	dec.CharsetReader = charset.reader
	for {
		tok, err := dec.RawToken()
		if err != nil {
			switch {
			case charset.err != nil:
				err = charset.err
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

// charsets is the Decoder.CharsetReader of this package: reader accepts
// the encodings that are read as UTF-8, and keeps the error for any other
// in err, as encoding/xml formats it with %v and errors.Is would not find
// it.
type charsets struct{ err error }

func (c *charsets) reader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(charset) {
	case "utf-8", "us-ascii":
		return input, nil
	}
	c.err = fmt.Errorf("%w, not %s", ErrUnsupportedEncoding, charset)
	return nil, c.err
}

// DepthError reports elements nested deeper than CheckWellFormed allows.
type DepthError struct {
	Max  int // the limit
	Line int // of the start tag that exceeds it
}

func (e *DepthError) Error() string {
	return fmt.Sprintf("line %d: elements are nested deeper than %d", e.Line, e.Max)
}

// ctxCheckInterval is the number of tokens between checks of the context.
const ctxCheckInterval = 1 << 10

// CheckWellFormed reads a whole document and fails on the first thing that
// keeps it from being well-formed XML with well-formed namespaces, as far
// as it checks:
//
//   - a syntax error of encoding/xml in strict mode, such as a tag that is
//     not closed, a mismatched end tag, an unknown entity or a character
//     that XML does not allow;
//   - no root element, a second root element, or text outside the root;
//   - an XML declaration that is not at the very start (a byte order mark
//     may precede it), or a processing instruction whose target is xml in
//     another case, which is reserved;
//   - an attribute that occurs twice on an element, by its name as written
//     or by namespace and local name;
//   - a prefix of an element or an attribute that no xmlns attribute in
//     scope declares, and xmlns:p="", which Namespaces in XML 1.0 does not
//     allow;
//   - a DOCTYPE declaration, which fails with ErrDTD;
//   - a declared encoding other than UTF-8 or US-ASCII, which fails with
//     ErrUnsupportedEncoding, as in DetectRoot.
//
// It does not check everything a validating parser would: names are those
// encoding/xml accepts, and the other values of xmlns attributes are not
// looked at. encoding/xml reads XML 1.0 only: a document that declares
// version 1.1 is rejected with the error of the decoder. No entity is
// loaded or expanded other than the five predefined ones and character
// references.
//
// A violation is an *xml.SyntaxError with its line. Elements nested deeper
// than maxDepth fail with a *DepthError; zero or less sets no limit. When
// ctx is done the check stops with its error. The work is in proportion to
// the size of the document, however deep it nests and however many
// attributes an element has.
func CheckWellFormed(ctx context.Context, r io.Reader, maxDepth int) error {
	dec := xml.NewDecoder(r)
	dec.Strict = true
	var charset charsets
	dec.CharsetReader = charset.reader
	c := wellFormedness{maxDepth: maxDepth, bound: map[string]string{"xml": namespaceXML}}
	for tokens := 0; ; tokens++ {
		if tokens%ctxCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		line, _ := dec.InputPos()
		offset := dec.InputOffset()
		tok, err := dec.RawToken()
		if errors.Is(err, io.EOF) {
			return c.end(line)
		}
		if err != nil {
			if charset.err != nil {
				return charset.err
			}
			return err
		}
		if err := c.token(tok, offset); err != nil {
			if msg, ok := errors.AsType[violation](err); ok {
				return &xml.SyntaxError{Msg: string(msg), Line: line}
			}
			if errors.Is(err, errTooDeep) {
				return &DepthError{Max: maxDepth, Line: line}
			}
			return err
		}
	}
}

const namespaceXML = "http://www.w3.org/XML/1998/namespace"

var errTooDeep = errors.New("nested too deep")

// violation is a well-formedness error that CheckWellFormed found itself.
type violation string

func (v violation) Error() string { return string(v) }

// wellFormedness is the state of CheckWellFormed between tokens.
type wellFormedness struct {
	maxDepth int
	open     []xml.Name // the open elements, names as written
	// bound maps each prefix in scope to its namespace. An element that
	// declares a prefix records in undo what the prefix was bound to
	// before, and marks holds, per open element, the length of undo when it
	// started: closing the element restores what it changed. Resolving a
	// prefix is one lookup, however deep the element lies.
	bound map[string]string
	undo  []binding
	marks []int
	root  bool // the root element has started
	// started: a token other than a leading byte order mark was read, after
	// which an XML declaration is misplaced.
	started bool
	attrs   map[attrName]bool // reused for each element
}

// binding is what a prefix was bound to before an element declared it.
type binding struct {
	prefix, uri string
	bound       bool
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
		switch {
		case t.Target == "xml" && started:
			return violation("XML declaration is not at the start of the document")
		case t.Target != "xml" && strings.EqualFold(t.Target, "xml"):
			return violation("processing instruction target " + t.Target + " is reserved")
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
		c.close()
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
	if c.maxDepth > 0 && len(c.open) >= c.maxDepth {
		return errTooDeep
	}
	c.open, c.marks = append(c.open, t.Name), append(c.marks, len(c.undo))
	// The declarations of the element are in scope for its own name and
	// for all its attributes, wherever among them they stand. One that
	// occurs twice is found below, with the other attributes.
	for _, a := range t.Attr {
		if a.Name.Space != "xmlns" {
			continue
		}
		if a.Value == "" {
			return violation("xmlns:" + a.Name.Local + " is empty: a prefix cannot be undeclared")
		}
		before, bound := c.bound[a.Name.Local]
		c.undo = append(c.undo, binding{prefix: a.Name.Local, uri: before, bound: bound})
		c.bound[a.Name.Local] = a.Value
	}
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

// close ends the innermost open element and restores the prefixes it
// declared, the last declaration first.
func (c *wellFormedness) close() {
	last := len(c.open) - 1
	mark := c.marks[last]
	for i := len(c.undo) - 1; i >= mark; i-- {
		if b := c.undo[i]; b.bound {
			c.bound[b.prefix] = b.uri
		} else {
			delete(c.bound, b.prefix)
		}
	}
	c.open, c.marks, c.undo = c.open[:last], c.marks[:last], c.undo[:mark]
}

// resolve returns the namespace that the open elements bind the prefix to.
// No prefix needs no declaration, and xml is always declared.
func (c *wellFormedness) resolve(prefix string) (string, bool) {
	if prefix == "" {
		return "", true
	}
	uri, ok := c.bound[prefix]
	return uri, ok
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
