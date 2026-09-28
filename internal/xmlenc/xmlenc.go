// Package xmlenc converts XML documents to UTF-8, the only encoding the XSD
// library and the Schematron extractor read.
package xmlenc

import (
	"bytes"
	"errors"
	"unicode/utf16"
	"unicode/utf8"
)

// ToUTF8 transcodes UTF-16 with a byte order mark to UTF-8, and returns
// other input unchanged.
func ToUTF8(data []byte) ([]byte, error) {
	if len(data) < 2 || !(data[0] == 0xFF && data[1] == 0xFE || data[0] == 0xFE && data[1] == 0xFF) {
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
	// The XML declaration, if any, still names UTF-16.
	out = bytes.Replace(out, []byte(`encoding="UTF-16"`), []byte(`encoding="UTF-8"`), 1)
	return out, nil
}
