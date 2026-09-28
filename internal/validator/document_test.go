package validator

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestDetectRoot(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want xml.Name
	}{
		{"default namespace", `<qti-assessment-item xmlns="` + NamespaceASI + `"/>`,
			xml.Name{Space: NamespaceASI, Local: "qti-assessment-item"}},
		{"prefixed", `<?xml version="1.0"?><!-- c --><?pi x?><q:qti-assessment-test xmlns:q="` + NamespaceASI + `"/>`,
			xml.Name{Space: NamespaceASI, Local: "qti-assessment-test"}},
		{"no namespace", `<qti-assessment-item/>`, xml.Name{Local: "qti-assessment-item"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, rest, err := detectRoot(strings.NewReader(tt.doc))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			replayed, err := io.ReadAll(rest)
			if err != nil || string(replayed) != tt.doc {
				t.Fatalf("replay = %q, %v; want the whole document", replayed, err)
			}
		})
	}
}

func TestDetectRootReplaysLongDocuments(t *testing.T) {
	doc := `<qti-assessment-item xmlns="` + NamespaceASI + `">` + strings.Repeat("<p>x</p>", 20000) + `</qti-assessment-item>`
	_, rest, err := detectRoot(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	replayed, _ := io.ReadAll(rest)
	if string(replayed) != doc {
		t.Fatal("replayed document differs from input")
	}
}

func TestDetectRootErrors(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want error
	}{
		{"undeclared prefix", `<q:qti-assessment-item/>`, nil},
		{"DTD", `<!DOCTYPE x><x/>`, errDTD},
		{"encoding", `<?xml version="1.0" encoding="UTF-16"?><x/>`, errUnsupportedEncoding},
		{"long prolog", `<!--` + strings.Repeat("x", maxPrologBytes) + `--><x/>`, errPrologTooLong},
		{"empty", ``, nil},
		{"text only", `hello`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := detectRoot(strings.NewReader(tt.doc))
			if err == nil {
				t.Fatal("want an error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
}

func TestLookupDocumentTypeNeedsNamespace(t *testing.T) {
	if _, ok := lookupDocumentType(xml.Name{Local: "qti-assessment-item"}); ok {
		t.Fatal("accepted qti-assessment-item without the QTI namespace")
	}
	if dt, ok := lookupDocumentType(xml.Name{Space: NamespaceManifest, Local: "manifest"}); !ok || dt.Schema != "imscp-manifest" {
		t.Fatalf("got %+v, %v", dt, ok)
	}
}
