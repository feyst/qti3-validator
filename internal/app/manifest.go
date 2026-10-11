package app

import (
	"bytes"
	"encoding/xml"
	"strings"

	"qti3-validator/internal/domain/qti"
	"qti3-validator/internal/lib/xmldoc"
)

// manifestSchemaVersion reads manifest/metadata/schemaversion. It returns an
// empty value when the element is absent or the document cannot be read;
// the schema reports those problems.
func manifestSchemaVersion(data []byte) qti.DeclaredVersion {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var stack []string
	for {
		line, col := dec.InputPos()
		tok, err := dec.Token()
		if err != nil {
			return qti.DeclaredVersion{}
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != qti.NamespaceManifest {
				stack = append(stack, "")
				continue
			}
			stack = append(stack, t.Name.Local)
			if len(stack) == 3 && stack[0] == "manifest" && stack[1] == "metadata" && stack[2] == "schemaversion" {
				var value string
				if err := dec.DecodeElement(&value, &t); err != nil {
					return qti.DeclaredVersion{}
				}
				return qti.DeclaredVersion{Value: strings.TrimSpace(value), Line: line, Column: col}
			}
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		}
	}
}

// A manifestFile is what the manifest says of a file of one of its QTI
// resources.
type manifestFile struct {
	kind qti.ResourceKind
	// href reports that the file is the href of a resource: the document
	// the resource is, not merely one of its files.
	href bool
}

// manifestResources says, for the files of the manifest's QTI resources,
// what they must be, by their path inside the package. xml:base is applied
// as the reference checks apply it. The href of a resource is what its type
// says; a file of a resource is a fragment when any fragment resource lists
// it, so an item may list the fragments it includes among its files.
func manifestResources(data []byte) map[string]manifestFile {
	hrefs := map[string]qti.ResourceKind{}
	files := map[string]qti.ResourceKind{}
	dec := xml.NewDecoder(bytes.NewReader(data))
	var bases qti.BaseStack
	kind := qti.ResourceOther
	add := func(to map[string]qti.ResourceKind, base, href string) {
		target, ok := qti.PackagePath(qti.ManifestName, qti.JoinBase(base, href))
		if ok && kind != qti.ResourceOther && to[target] != qti.ResourceFragment {
			to[target] = kind
		}
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != qti.NamespaceManifest {
				continue
			}
			base := bases.Push(xmlBase(t))
			switch t.Name.Local {
			case "resource":
				kind = qti.ClassifyResourceType(xmldoc.Attr(t, "type"))
				add(hrefs, base, xmldoc.Attr(t, "href"))
			case "file":
				add(files, base, xmldoc.Attr(t, "href"))
			}
		case xml.EndElement:
			if t.Name.Space != qti.NamespaceManifest {
				continue
			}
			bases.Pop()
			if t.Name.Local == "resource" {
				kind = qti.ResourceOther
			}
		}
	}
	out := make(map[string]manifestFile, len(hrefs)+len(files))
	for f, k := range files {
		out[f] = manifestFile{kind: k}
	}
	for f, k := range hrefs {
		out[f] = manifestFile{kind: k, href: true}
	}
	return out
}

// xmlBase returns the xml:base of an element.
func xmlBase(t xml.StartElement) string {
	for _, a := range t.Attr {
		if a.Name.Space == "http://www.w3.org/XML/1998/namespace" && a.Name.Local == "base" {
			return a.Value
		}
	}
	return ""
}
