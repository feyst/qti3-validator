// Package xmlenc converts XML documents to UTF-8, the only encoding the XSD
// library and the Schematron extractor read.
package xmlenc

import (
	"bytes"
	"errors"
	"regexp"
	"unicode/utf16"
	"unicode/utf8"
)

// ToUTF8 transcodes UTF-16 with a byte order mark to UTF-8, and returns
// other input unchanged. An XML declaration that names UTF-16, UTF-16LE or
// UTF-16BE then names UTF-8, so the result can be read as it stands.
func ToUTF8(data []byte) ([]byte, error) {
	if len(data) < 2 || (data[0] != 0xFF || data[1] != 0xFE) && (data[0] != 0xFE || data[1] != 0xFF) {
		return data, nil
	}
	bigEndian := data[0] == 0xFE
	data = data[2:]
	if len(data)%2 != 0 {
		return nil, errors.New("odd UTF-16 byte length")
	}
	units := make([]uint16, len(data)/2)
	for i := range units {
		if bigEndian {
			units[i] = uint16(data[2*i])<<8 | uint16(data[2*i+1])
		} else {
			units[i] = uint16(data[2*i+1])<<8 | uint16(data[2*i])
		}
	}
	out := make([]byte, 0, len(units))
	for _, r := range utf16.Decode(units) {
		out = utf8.AppendRune(out, r)
	}
	return declareUTF8(out), nil
}

// declaredUTF16 matches the encoding pseudo-attribute of an XML declaration
// that names UTF-16, in either quote style and any case.
var declaredUTF16 = regexp.MustCompile(`(?i)\sencoding\s*=\s*("utf-16(?:le|be)?"|'utf-16(?:le|be)?')`)

// declareUTF8 rewrites the encoding that the XML declaration of a
// transcoded document names, if it has one that still names UTF-16. Only
// the declaration is looked at: the same text further on is content.
func declareUTF8(doc []byte) []byte {
	const open = "<?xml"
	if len(doc) <= len(open) || string(doc[:len(open)]) != open || !isSpace(doc[len(open)]) {
		return doc
	}
	end := bytes.Index(doc, []byte("?>"))
	if end < 0 {
		return doc
	}
	loc := declaredUTF16.FindSubmatchIndex(doc[:end])
	if loc == nil {
		return doc
	}
	out := make([]byte, 0, len(doc))
	out = append(out, doc[:loc[2]]...)
	out = append(out, `"UTF-8"`...)
	return append(out, doc[loc[3]:]...)
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }
