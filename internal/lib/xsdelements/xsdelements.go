// Package xsdelements reads the names of the elements an XML Schema
// declares, and stores them in a compiled form that loads without reading
// the schema again.
package xsdelements

import (
	"compress/gzip"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
)

const namespaceXSD = "http://www.w3.org/2001/XMLSchema"

// Elements are the names of the elements a schema declares in its target
// namespace, each list sorted. Elements of included or imported schemas are
// not part of it.
type Elements struct {
	Namespace string `json:"namespace"`
	// Global are the elements declared at the top level.
	Global []string `json:"global"`
	// Local are the elements declared inside a type or a group.
	Local []string `json:"local"`
	// ManyTyped are the elements declared with more than one type.
	ManyTyped []string `json:"many_typed"`
}

// Read reads the names of the elements the schema in r, which is UTF-8,
// declares in its target namespace: at the top level, and locally inside its
// types and groups, and which of them it declares with more than one type.
func Read(r io.Reader) (Elements, error) {
	global, local := map[string]bool{}, map[string]bool{}
	types := declaredTypes{byName: map[string]map[string]bool{}, manyTyped: map[string]bool{}}
	var namespace string
	rooted := false
	dec := xml.NewDecoder(r)
	qualified := false // whether local elements are in the target namespace
	for depth := 0; ; {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			if !rooted {
				return Elements{}, errors.New("no root element")
			}
			return Elements{Namespace: namespace, Global: sorted(global), Local: sorted(local), ManyTyped: sorted(types.manyTyped)}, nil
		}
		if err != nil {
			return Elements{}, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch {
			case depth == 1:
				if t.Name != (xml.Name{Space: namespaceXSD, Local: "schema"}) {
					return Elements{}, errors.New("root element is not xs:schema")
				}
				rooted = true
				namespace = attr(t, "targetNamespace")
				qualified = attr(t, "elementFormDefault") == "qualified"
			case t.Name != xml.Name{Space: namespaceXSD, Local: "element"} || attr(t, "name") == "":
			case depth == 2:
				global[attr(t, "name")] = true
				types.declare(t)
			case attr(t, "form") == "qualified" || qualified && attr(t, "form") == "":
				local[attr(t, "name")] = true
				types.declare(t)
			}
		case xml.EndElement:
			depth--
		}
	}
}

// declaredTypes are the types every element name is declared with.
type declaredTypes struct {
	byName    map[string]map[string]bool
	anonymous int
	manyTyped map[string]bool // the names with more than one type
}

// declare adds the declaration t. One without a type attribute has a type
// of its own.
func (d *declaredTypes) declare(t xml.StartElement) {
	name, typ := attr(t, "name"), attr(t, "type")
	if typ == "" {
		d.anonymous++
		typ = fmt.Sprintf("#%d", d.anonymous)
	}
	if d.byName[name] == nil {
		d.byName[name] = map[string]bool{}
	}
	d.byName[name][typ] = true
	if len(d.byName[name]) > 1 {
		d.manyTyped[name] = true
	}
}

func attr(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if a.Name.Space == "" && a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// sorted returns the names of set in order, never nil, so that a set
// without names is stored as an empty list.
func sorted(set map[string]bool) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// CompiledVersion changes when the Compiled format does, or when Read reads
// other names from the same schema.
const CompiledVersion = 1

// Compiled holds the elements of schemas, read at build time.
type Compiled struct {
	Version int `json:"version"`
	// Schemas are the schemas by URL.
	Schemas map[string]Schema `json:"schemas"`
}

// Schema is the elements of one schema file.
type Schema struct {
	// SHA256 is the checksum of the file the elements were read from, in
	// hexadecimal: the elements are those of that file and no other.
	SHA256 string `json:"sha256"`
	Elements
}

// Encode writes c gzip-compressed.
func (c *Compiled) Encode(w io.Writer) error {
	zw, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(zw).Encode(c); err != nil {
		return err
	}
	return zw.Close()
}

// Decode reads what Encode wrote. It refuses another format version.
func Decode(r io.Reader) (*Compiled, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	var c Compiled
	if err := json.NewDecoder(zr).Decode(&c); err != nil {
		return nil, err
	}
	if c.Version != CompiledVersion {
		return nil, fmt.Errorf("compiled elements have format version %d, want %d", c.Version, CompiledVersion)
	}
	return &c, nil
}
