package validator

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Namespaces of the documents this validator accepts.
const (
	NamespaceASI      = "http://www.imsglobal.org/xsd/imsqtiasi_v3p0"
	NamespaceManifest = "http://www.imsglobal.org/xsd/qti/qtiv3p0/imscp_v1p1"
	NamespaceLOM      = "http://ltsc.ieee.org/xsd/LOM"
	NamespaceMetadata = "http://www.imsglobal.org/xsd/imsqti_metadata_v3p0"
)

// DocumentType is a root element this validator accepts, and the name it
// reports as the schema.
type DocumentType struct {
	Namespace string
	Root      string
	Schema    string
}

// DocumentTypes lists the accepted root elements. Matching uses both the
// namespace and the local name, so <qti-assessment-item> in another namespace
// is rejected. Every root must be a global element in the compiled schemas.
var DocumentTypes = []DocumentType{
	{NamespaceASI, "qti-assessment-item", "qti-assessment-item"},
	{NamespaceASI, "qti-assessment-test", "qti-assessment-test"},
	{NamespaceASI, "qti-assessment-section", "qti-assessment-section"},
	{NamespaceASI, "qti-assessment-stimulus", "qti-assessment-stimulus"},
	{NamespaceASI, "qti-response-processing", "qti-response-processing"},
	{NamespaceManifest, "manifest", "imscp-manifest"},
	{NamespaceLOM, "lom", "lom"},
	{NamespaceMetadata, "qtiMetadata", "qti-metadata"},
}

func lookupDocumentType(name xml.Name) (DocumentType, bool) {
	for _, t := range DocumentTypes {
		if t.Namespace == name.Space && t.Root == name.Local {
			return t, true
		}
	}
	return DocumentType{}, false
}

// maxPrologBytes bounds how far the root element may be from the start of
// the document (XML declaration, comments, processing instructions).
const maxPrologBytes = 64 << 10

var (
	errPrologTooLong       = errors.New("no root element in the first 64 KiB")
	errUnsupportedEncoding = errors.New("only UTF-8 is supported")
	errDTD                 = errors.New("DOCTYPE declarations are not allowed")
)

// detectRoot reads up to the root start tag and returns its name, plus a
// reader that replays the consumed bytes followed by the rest of r.
func detectRoot(r io.Reader) (xml.Name, io.Reader, error) {
	rec := &recorder{r: r}
	dec := xml.NewDecoder(rec)
	dec.Strict = true
	var charsetErr error
	dec.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		switch strings.ToLower(charset) {
		case "utf-8", "us-ascii":
			return input, nil
		}
		// encoding/xml formats this error with %v, so keep it for errors.Is.
		charsetErr = fmt.Errorf("%w, not %s", errUnsupportedEncoding, charset)
		return nil, charsetErr
	}
	for {
		tok, err := dec.RawToken()
		if err != nil {
			switch {
			case charsetErr != nil:
				err = charsetErr
			case rec.overflow:
				err = errPrologTooLong
			case errors.Is(err, io.EOF):
				err = errors.New("document has no root element")
			}
			return xml.Name{}, nil, err
		}
		switch t := tok.(type) {
		case xml.Directive:
			return xml.Name{}, nil, errDTD
		case xml.StartElement:
			name, err := resolveRootName(t)
			if err != nil {
				return xml.Name{}, nil, err
			}
			return name, io.MultiReader(bytes.NewReader(rec.buf), r), nil
		}
	}
}

// resolveRootName resolves the namespace of the root element from its own
// declarations; RawToken leaves prefixes unresolved, and for the root only its
// own attributes can bind them.
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

// recorder keeps the bytes read through it, up to maxPrologBytes.
type recorder struct {
	r        io.Reader
	buf      []byte
	overflow bool
}

func (rec *recorder) Read(p []byte) (int, error) {
	if len(rec.buf) >= maxPrologBytes {
		rec.overflow = true
		return 0, errPrologTooLong
	}
	if room := maxPrologBytes - len(rec.buf); len(p) > room {
		p = p[:room]
	}
	n, err := rec.r.Read(p)
	rec.buf = append(rec.buf, p[:n]...)
	return n, err
}

// checkWellFormed reads a whole document with the standard library decoder,
// which neither loads external entities nor expands DTD-declared ones.
func checkWellFormed(r io.Reader) error {
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
			return errDTD
		}
	}
}

// schemaVersionPath is where a manifest declares its QTI version, in the
// form the XSD validator reports paths.
const schemaVersionPath = "/manifest/metadata/schemaversion"

// schemaVersion is the version a manifest declares, and where.
type schemaVersion struct {
	value        string
	line, column int
}

// manifestSchemaVersion reads manifest/metadata/schemaversion. It returns
// an empty value when the element is absent or the document cannot be read;
// the schema reports those problems.
func manifestSchemaVersion(data []byte) schemaVersion {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var stack []string
	for {
		line, col := dec.InputPos()
		tok, err := dec.Token()
		if err != nil {
			return schemaVersion{}
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != NamespaceManifest {
				stack = append(stack, "")
				continue
			}
			stack = append(stack, t.Name.Local)
			if len(stack) == 3 && stack[0] == "manifest" && stack[1] == "metadata" && stack[2] == "schemaversion" {
				var value string
				if err := dec.DecodeElement(&value, &t); err != nil {
					return schemaVersion{}
				}
				return schemaVersion{value: strings.TrimSpace(value), line: line, column: col}
			}
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		}
	}
}
