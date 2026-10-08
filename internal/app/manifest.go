package app

import (
	"bytes"
	"encoding/xml"
	"net/url"
	"path"
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

// manifestQTIFiles lists the href and file hrefs of the manifest's QTI
// resources, as paths inside the package.
func manifestQTIFiles(data []byte) map[string]bool {
	files := map[string]bool{}
	dec := xml.NewDecoder(bytes.NewReader(data))
	inQTI := false
	add := func(href string) {
		if href == "" {
			return
		}
		if u, err := url.PathUnescape(href); err == nil {
			href = u
		}
		files[path.Clean(href)] = true
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return files
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "resource":
				inQTI = qti.IsQTIResourceType(xmldoc.Attr(t, "type"))
				if inQTI {
					add(xmldoc.Attr(t, "href"))
				}
			case "file":
				if inQTI {
					add(xmldoc.Attr(t, "href"))
				}
			}
		case xml.EndElement:
			if t.Name.Local == "resource" {
				inQTI = false
			}
		}
	}
}
