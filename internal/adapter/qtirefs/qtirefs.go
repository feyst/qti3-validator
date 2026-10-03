// Package qtirefs reads what a QTI or package manifest document refers to:
// other documents, media, stylesheets, PCI modules and catalog files, and
// for tests the variables of their items. It implements app.ReferenceReader;
// the checks themselves are qti.CheckReferences.
package qtirefs

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"strings"

	"github.com/kennisnet/qti3-validator/internal/app"
	"github.com/kennisnet/qti3-validator/internal/domain/qti"
)

const (
	namespaceXML      = "http://www.w3.org/XML/1998/namespace"
	namespaceXInclude = "http://www.w3.org/2001/XInclude"
)

// Reader implements app.ReferenceReader.
type Reader struct{}

var _ app.ReferenceReader = Reader{}

// assetAttributes are the attributes of HTML elements in QTI content that
// name a file to load.
var assetAttributes = map[string][]string{
	"img":    {"src"},
	"object": {"data"},
	"audio":  {"src"},
	"video":  {"src", "poster"},
	"source": {"src"},
	"track":  {"src"},
}

// ReadReferences implements app.ReferenceReader. data must be well-formed.
func (Reader) ReadReferences(data []byte) (qti.DocumentReferences, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	r := &reader{}
	for {
		line, col := dec.InputPos()
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return r.refs, nil
		}
		if err != nil {
			return r.refs, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			r.start(t, line, col)
		case xml.CharData:
			if r.inCard {
				r.cardText.Write(t)
			}
		case xml.EndElement:
			r.end(t)
		}
	}
}

// reader collects the references of one document.
type reader struct {
	refs     qti.DocumentReferences
	root     xml.Name
	bases    []string // xml:base of the enclosing manifest elements
	inCard   bool     // inside qti-file-href, whose text is the reference
	cardLine int
	cardCol  int
	cardText strings.Builder
}

func (r *reader) start(t xml.StartElement, line, col int) {
	if r.root.Local == "" {
		r.root = t.Name
		r.refs.Root = t.Name.Local
	}
	add := func(kind qti.RefKind, href string) {
		if href != "" {
			r.refs.References = append(r.refs.References, qti.Reference{
				Kind: kind, Href: href, Element: t.Name.Local, Line: line, Column: col,
			})
		}
	}
	switch t.Name.Space {
	case qti.NamespaceManifest:
		base := attr(t, namespaceXML, "base")
		if len(r.bases) > 0 {
			base = path.Join(r.bases[len(r.bases)-1], base)
		}
		r.bases = append(r.bases, base)
		if t.Name.Local == "resource" || t.Name.Local == "file" {
			add(qti.RefManifestFile, joinBase(base, attr(t, "", "href")))
		}
	case namespaceXInclude:
		if t.Name.Local == "include" {
			add(qti.RefAsset, attr(t, "", "href"))
		}
	case qti.NamespaceASI:
		readASI(&r.refs, t, r.root, line, col, add)
		if t.Name.Local == "qti-file-href" {
			r.inCard, r.cardLine, r.cardCol = true, line, col
			r.cardText.Reset()
		}
	}
}

func (r *reader) end(t xml.EndElement) {
	if t.Name.Space == qti.NamespaceManifest && len(r.bases) > 0 {
		r.bases = r.bases[:len(r.bases)-1]
	}
	if r.inCard && t.Name.Local == "qti-file-href" {
		r.inCard = false
		if href := strings.TrimSpace(r.cardText.String()); href != "" {
			r.refs.References = append(r.refs.References, qti.Reference{
				Kind: qti.RefCatalogFile, Href: href, Element: "qti-file-href", Line: r.cardLine, Column: r.cardCol,
			})
		}
	}
}

// readASI reads the references of an element in the QTI namespace.
func readASI(refs *qti.DocumentReferences, t xml.StartElement, root xml.Name, line, col int, add func(qti.RefKind, string)) {
	switch name := t.Name.Local; name {
	case "qti-assessment-item-ref":
		add(qti.RefItem, attr(t, "", "href"))
		refs.ItemRefs = append(refs.ItemRefs, qti.ItemRef{Identifier: attr(t, "", "identifier"), Href: attr(t, "", "href")})
	case "qti-assessment-section-ref":
		add(qti.RefSection, attr(t, "", "href"))
	case "qti-assessment-stimulus-ref":
		add(qti.RefStimulus, attr(t, "", "href"))
	case "qti-stylesheet":
		add(qti.RefAsset, attr(t, "", "href"))
	case "qti-response-processing":
		add(qti.RefTemplateLocation, attr(t, "", "template-location"))
	case "qti-interaction-modules":
		add(qti.RefModuleConfig, attr(t, "", "primary-configuration"))
		add(qti.RefModuleConfig, attr(t, "", "secondary-configuration"))
	case "qti-interaction-module":
		primary, fallback := attr(t, "", "primary-path"), attr(t, "", "fallback-path")
		if primary == "" {
			primary, fallback = fallback, ""
		}
		if primary != "" {
			refs.References = append(refs.References, qti.Reference{
				Kind: qti.RefModule, Href: primary, Fallback: fallback, Element: t.Name.Local, Line: line, Column: col,
			})
		}
	case "qti-response-declaration", "qti-outcome-declaration", "qti-template-declaration", "qti-context-declaration":
		if root.Local == "qti-assessment-item" {
			refs.Declared = append(refs.Declared, attr(t, "", "identifier"))
		}
	case "qti-variable":
		if root.Local == "qti-assessment-test" {
			if item, variable, ok := strings.Cut(attr(t, "", "identifier"), "."); ok {
				refs.VariableRefs = append(refs.VariableRefs, qti.VariableRef{Item: item, Variable: variable, Line: line, Column: col})
			}
		}
	default:
		for _, a := range assetAttributes[name] {
			add(qti.RefAsset, attr(t, "", a))
		}
	}
}

// joinBase applies an xml:base to a relative href. An href or base with a
// scheme points elsewhere and is left as it is.
func joinBase(base, href string) string {
	if base == "" || href == "" || strings.Contains(href, ":") || strings.Contains(base, ":") {
		return href
	}
	return path.Join(base, href)
}

func attr(t xml.StartElement, space, local string) string {
	for _, a := range t.Attr {
		if a.Name.Space == space && a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}
