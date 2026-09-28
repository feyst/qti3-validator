package validator

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jacoelho/xsd"
	"github.com/kennisnet/qti3-validator/internal/schematron"
	"github.com/kennisnet/qti3-validator/internal/xmlenc"
)

// Validators can be added after the fact by mounting a directory, named by
// Options.ValidatorsDir. Only its top-level files are read, once, at
// startup:
//
//   - *.sch: a standalone ISO Schematron schema. Its rules run on every
//     document, after the built-in rules.
//   - *.xsd: an XML Schema. Each global element it declares becomes a
//     document type of its own, validated against this XSD, the Schematron
//     rules embedded in it and the .sch files.
//
// Everything else is ignored. A file that does not compile fails New.

// maxValidatorFileSize bounds each file read from the validators directory.
// A variable for tests.
var maxValidatorFileSize int64 = 64 << 20

// namespaceXSD is the XML Schema namespace.
const namespaceXSD = "http://www.w3.org/2001/XMLSchema"

// embeddedSchemaPrefix is the only absolute schemaLocation a mounted XSD may
// use; it resolves to the embedded schemas.
const embeddedSchemaPrefix = "https://purl.imsglobal.org/"

// customType is a document type declared by a mounted XSD.
type customType struct {
	DocumentType
	file   string             // the XSD's name in the validators directory
	engine *xsd.Engine        // that XSD, with its includes and imports
	rules  *schematron.Engine // rules embedded in that XSD; nil without
}

// mounted is what the validators directory adds.
type mounted struct {
	types map[xml.Name]*customType
	rules *schematron.Engine // the .sch files; nil without
}

// validatorsDir reads files from the validators directory. os.Root keeps
// every read, symbolic links included, inside the directory.
type validatorsDir struct {
	path string
	root *os.Root
}

// read returns a regular top-level file, transcoded to UTF-8.
func (d validatorsDir) read(name string) ([]byte, error) {
	f, err := d.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxValidatorFileSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxValidatorFileSize {
		return nil, fmt.Errorf("larger than %d bytes", maxValidatorFileSize)
	}
	return xmlenc.ToUTF8(data)
}

// errorf names the file in the directory an error is about.
func (d validatorsDir) errorf(name, format string, args ...any) error {
	return fmt.Errorf("%s: %w", filepath.Join(d.path, name), fmt.Errorf(format, args...))
}

// loadMounted reads and compiles the validators directory. Includes and
// imports of mounted XSDs that point into the embedded schemas go through
// res.
func loadMounted(opts Options, res resolver) (*mounted, error) {
	root, err := os.OpenRoot(opts.ValidatorsDir)
	if err != nil {
		return nil, fmt.Errorf("validators directory: %w", err)
	}
	defer root.Close()
	dir := validatorsDir{path: opts.ValidatorsDir, root: root}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("validators directory: %w", err)
	}
	ignore := func(name, reason string) {
		if opts.OnValidatorIgnored != nil {
			opts.OnValidatorIgnored(name, reason)
		}
	}

	m := &mounted{types: map[xml.Name]*customType{}}
	var ruleSets []schematron.RuleSet
	type loadedFile struct {
		name  string
		roots []string
	}
	var loaded []loadedFile
	for _, entry := range entries {
		name := entry.Name()
		// Stat follows a symbolic link, such as a Kubernetes ConfigMap's,
		// as long as it stays inside the directory.
		info, err := root.Stat(name)
		if err != nil {
			return nil, dir.errorf(name, "%w", err)
		}
		switch {
		case info.IsDir():
			ignore(name, "subdirectory")
			continue
		case !info.Mode().IsRegular():
			ignore(name, "not a regular file")
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".xsd" && ext != ".sch" {
			ignore(name, "not an .xsd or .sch file")
			continue
		}
		data, err := dir.read(name)
		if err != nil {
			return nil, dir.errorf(name, "%w", err)
		}
		if ext == ".sch" {
			rs, err := extractSchematron(name, data)
			if err != nil {
				return nil, dir.errorf(name, "%w", err)
			}
			ruleSets = append(ruleSets, rs)
			loaded = append(loaded, loadedFile{name: name})
			continue
		}
		types, err := compileCustomXSD(dir, name, data, res)
		if err != nil {
			return nil, err
		}
		file := loadedFile{name: name}
		for _, ct := range types {
			key := xml.Name{Space: ct.Namespace, Local: ct.Root}
			if builtin, ok := lookupDocumentType(key); ok {
				return nil, dir.errorf(name, "root element {%s}%s is already the built-in document type %s",
					key.Space, key.Local, builtin.Schema)
			}
			if other, ok := m.types[key]; ok {
				return nil, dir.errorf(name, "root element {%s}%s is also declared by %s",
					key.Space, key.Local, other.file)
			}
			m.types[key] = ct
			file.roots = append(file.roots, "{"+key.Space+"}"+key.Local)
		}
		loaded = append(loaded, file)
	}
	if len(ruleSets) > 0 {
		// Precompile names the file of a rule that does not compile.
		compiled, err := schematron.Precompile(ruleSets...)
		if err != nil {
			return nil, fmt.Errorf("validators directory: %w", err)
		}
		if m.rules, err = schematron.New(compiled); err != nil {
			return nil, fmt.Errorf("validators directory: %w", err)
		}
	}
	if opts.OnValidatorLoaded != nil {
		for _, f := range loaded {
			opts.OnValidatorLoaded(f.name, f.roots)
		}
	}
	return m, nil
}

// extractSchematron reads a standalone Schematron schema.
func extractSchematron(name string, data []byte) (schematron.RuleSet, error) {
	root, _, err := detectRoot(bytes.NewReader(data))
	if err != nil {
		return schematron.RuleSet{}, err
	}
	if root != (xml.Name{Space: schematron.NamespaceSchematron, Local: "schema"}) {
		return schematron.RuleSet{}, fmt.Errorf("root element {%s}%s is not sch:schema", root.Space, root.Local)
	}
	return schematron.Extract(name, bytes.NewReader(data))
}

// compileCustomXSD compiles a mounted XSD and the Schematron rules embedded
// in it, and returns a document type for each global element it declares.
func compileCustomXSD(dir validatorsDir, name string, data []byte, res resolver) ([]*customType, error) {
	roots, err := globalElements(data)
	if err != nil {
		return nil, dir.errorf(name, "%w", err)
	}
	src := xsd.Bytes(name, data).WithResolver(mountedResolver{dir: dir, embedded: res})
	engine, err := xsd.Compile(src)
	if err != nil {
		return nil, dir.errorf(name, "%w", err)
	}
	rs, err := schematron.Extract(name, bytes.NewReader(data))
	if err != nil {
		return nil, dir.errorf(name, "%w", err)
	}
	compiled, err := schematron.Precompile(rs)
	if err != nil {
		return nil, dir.errorf(name, "%w", err)
	}
	var rules *schematron.Engine
	if len(compiled.Sets) > 0 {
		if rules, err = schematron.New(compiled); err != nil {
			return nil, dir.errorf(name, "%w", err)
		}
	}
	types := make([]*customType, len(roots))
	for i, root := range roots {
		types[i] = &customType{
			DocumentType: DocumentType{Namespace: root.Space, Root: root.Local, Schema: root.Local},
			file:         name,
			engine:       engine,
			rules:        rules,
		}
	}
	return types, nil
}

// globalElements returns the elements an XSD declares at the top level, in
// its target namespace. Elements of included or imported schemas are not
// part of it.
func globalElements(data []byte) ([]xml.Name, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var (
		names     []xml.Name
		namespace string
		depth     int
	)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return names, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch {
			case depth == 1:
				if t.Name != (xml.Name{Space: namespaceXSD, Local: "schema"}) {
					return nil, fmt.Errorf("root element {%s}%s is not xs:schema", t.Name.Space, t.Name.Local)
				}
				namespace = attrValue(t, "targetNamespace")
			case depth == 2 && t.Name == xml.Name{Space: namespaceXSD, Local: "element"}:
				names = append(names, xml.Name{Space: namespace, Local: attrValue(t, "name")})
			}
		case xml.EndElement:
			depth--
		}
	}
}

func attrValue(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if a.Name.Space == "" && a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// mountedResolver resolves the includes and imports of a mounted XSD. A
// relative location names a file next to it in the validators directory; an
// https://purl.imsglobal.org/ URL, and any location inside such a schema, an
// embedded schema. Anything else fails: nothing is read from elsewhere on
// disk or fetched over the network.
type mountedResolver struct {
	dir      validatorsDir
	embedded resolver
}

// ResolveSchema implements xsd.Resolver.
func (r mountedResolver) ResolveSchema(base, location string) (xsd.SchemaSource, error) {
	if strings.HasPrefix(base, embeddedSchemaPrefix) || strings.Contains(location, "://") {
		ref, err := url.Parse(location)
		if err != nil {
			return xsd.SchemaSource{}, err
		}
		if baseURL, err := url.Parse(base); err == nil && baseURL.IsAbs() {
			ref = baseURL.ResolveReference(ref)
		}
		if !strings.HasPrefix(ref.String(), embeddedSchemaPrefix) {
			return xsd.SchemaSource{}, fmt.Errorf("schema location %q is neither a file in the validators directory nor an %s URL",
				location, embeddedSchemaPrefix)
		}
		return r.embedded.source(ref.String())
	}
	if location == "" || location == "." || location == ".." || strings.ContainsAny(location, `/\`) {
		return xsd.SchemaSource{}, fmt.Errorf("schema location %q is not a file in the validators directory", location)
	}
	data, err := r.dir.read(location)
	if err != nil {
		return xsd.SchemaSource{}, r.dir.errorf(location, "%w", err)
	}
	return xsd.Bytes(location, data), nil
}
