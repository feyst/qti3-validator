// Package xmldoc inspects XML documents safely with the standard library:
// it finds the root element without reading the whole document, and checks
// well-formedness without loading external entities or expanding DTD-declared
// ones.
package xmldoc

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
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

// CheckWellFormed reads a whole document. It fails on the first syntax error
// and on a DOCTYPE declaration.
func CheckWellFormed(r io.Reader) error {
	dec := xml.NewDecoder(r)
	dec.Strict = true
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, ok := tok.(xml.Directive); ok {
			return ErrDTD
		}
	}
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
