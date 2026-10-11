package xmlenc

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestToUTF8(t *testing.T) {
	le := []byte{0xFF, 0xFE, '<', 0, 'a', 0, 0xAC, 0x20, '/', 0, '>', 0}
	be := []byte{0xFE, 0xFF, 0, '<', 0, 'a', 0x20, 0xAC, 0, '/', 0, '>'}
	for _, in := range [][]byte{le, be, []byte("<a€/>")} {
		got, err := ToUTF8(in)
		if err != nil || string(got) != "<a€/>" {
			t.Fatalf("got %q, %v", got, err)
		}
	}
}

// utf16LE encodes s as UTF-16 with a little-endian byte order mark.
func utf16LE(s string) []byte {
	out := []byte{0xFF, 0xFE}
	for _, u := range utf16.Encode([]rune(s)) {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

// The XML declaration of a transcoded document names UTF-8, however it
// named UTF-16, and nothing else in the document is rewritten.
func TestToUTF8RewritesTheDeclaredEncoding(t *testing.T) {
	tests := []struct{ in, want string }{
		{`<?xml version="1.0" encoding="UTF-16"?><a/>`, `<?xml version="1.0" encoding="UTF-8"?><a/>`},
		{`<?xml version='1.0' encoding='UTF-16'?><a/>`, `<?xml version='1.0' encoding="UTF-8"?><a/>`},
		{`<?xml version="1.0" encoding="utf-16"?><a/>`, `<?xml version="1.0" encoding="UTF-8"?><a/>`},
		{`<?xml version="1.0" encoding="UTF-16LE"?><a/>`, `<?xml version="1.0" encoding="UTF-8"?><a/>`},
		{`<?xml version="1.0" encoding='utf-16be' standalone="yes"?><a/>`, `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><a/>`},
		{"<?xml version=\"1.0\"\n\tencoding = \"Utf-16\" ?><a/>", "<?xml version=\"1.0\"\n\tencoding = \"UTF-8\" ?><a/>"},
		// Only the declaration: the same text further on is content.
		{`<a>encoding="UTF-16"</a>`, `<a>encoding="UTF-16"</a>`},
		{`<?xml version="1.0"?><a b=' encoding="UTF-16"'/>`, `<?xml version="1.0"?><a b=' encoding="UTF-16"'/>`},
		{
			`<?xml version="1.0" encoding="UTF-16"?><a> encoding="UTF-16"<?p encoding="UTF-16"?></a>`,
			`<?xml version="1.0" encoding="UTF-8"?><a> encoding="UTF-16"<?p encoding="UTF-16"?></a>`,
		},
		{`<?xml-stylesheet encoding="UTF-16"?><a/>`, `<?xml-stylesheet encoding="UTF-16"?><a/>`},
		{`<?xml version="1.0" encoding="UTF-16`, `<?xml version="1.0" encoding="UTF-16`},
	}
	for _, tt := range tests {
		got, err := ToUTF8(utf16LE(tt.in))
		if err != nil || string(got) != tt.want {
			t.Errorf("%s: got %s, %v; want %s", tt.in, got, err, tt.want)
		}
	}
	// What matters: the standard decoder, which reads only UTF-8, reads the
	// result.
	for _, tt := range tests[:6] {
		got, err := ToUTF8(utf16LE(tt.in))
		if err != nil {
			t.Fatal(err)
		}
		dec := xml.NewDecoder(strings.NewReader(string(got)))
		for err == nil {
			_, err = dec.Token()
		}
		if !errors.Is(err, io.EOF) {
			t.Errorf("%s: the transcoded document does not parse: %v", tt.in, err)
		}
	}
	// A document that was UTF-8 already is left as it is.
	const plain = `<?xml version="1.0" encoding="UTF-16"?><a/>`
	if got, err := ToUTF8([]byte(plain)); err != nil || string(got) != plain {
		t.Errorf("got %s, %v", got, err)
	}
}
