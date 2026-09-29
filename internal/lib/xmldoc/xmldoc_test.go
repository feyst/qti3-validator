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
