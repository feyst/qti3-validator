// Package validatorsdir loads validators added after the fact by mounting a
// directory. Only its top-level files are read, once, at startup:
//
//   - *.sch: a standalone ISO Schematron schema. Its rules run on every
//     document, after the built-in rules.
//   - *.xsd: an XML Schema. Each global element it declares becomes a
//     document type of its own, validated against this XSD, the Schematron
//     rules embedded in it and the .sch files.
//
// Everything else is ignored. A file that does not compile fails Load.
package validatorsdir

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

	"qti3-validator/internal/adapter/rules"
	"qti3-validator/internal/adapter/xsdschema"
	"qti3-validator/internal/domain/qti"
	"qti3-validator/internal/lib/schematron"
	"qti3-validator/internal/lib/xmldoc"
	"qti3-validator/internal/lib/xmlenc"
)

// maxValidatorFileSize bounds each file read from the validators directory.
// A variable for tests.
var maxValidatorFileSize int64 = 64 << 20

// namespaceXSD is the XML Schema namespace.
const namespaceXSD = "http://www.w3.org/2001/XMLSchema"

// embeddedSchemaPrefix is the only absolute schemaLocation a mounted XSD may
// use; it resolves to the embedded schemas.
const embeddedSchemaPrefix = "https://purl.imsglobal.org/"

// Type is a document type declared by a mounted XSD.
type Type struct {
	qti.DocumentType
	File   string             // the XSD's name in the validators directory
	Schema *xsdschema.Checker // that XSD, with its includes and imports
	Rules  *rules.Checker     // rules embedded in that XSD; nil without
}

// Validators is what the validators directory adds.
type Validators struct {
	Types []Type
	Rules *rules.Checker // the .sch files; nil without
}

// Options configure Load.
type Options struct {
	Dir string
	// Embedded resolves includes and imports of the QTI schemas.
	Embedded xsdschema.Resolver
	// Loaded, if set, is called for every file loaded, with the roots of the
	// document types an XSD adds.
	Loaded func(file string, roots []string)
	// Ignored, if set, is called for every entry that is not used.
	Ignored func(file, reason string)
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

// Load reads and compiles the validators directory.
func Load(opts Options) (*Validators, error) {
	root, err := os.OpenRoot(opts.Dir)
	if err != nil {
		return nil, fmt.Errorf("validators directory: %w", err)
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("validators directory: %w", err)
	}
	l := &loader{opts: opts, dir: validatorsDir{path: opts.Dir, root: root}, declared: map[xml.Name]string{}}
	for _, entry := range entries {
		if err := l.add(entry.Name()); err != nil {
			return nil, err
		}
	}
	return l.finish()
}

// loader collects what the files of the directory add.
type loader struct {
	opts     Options
	dir      validatorsDir
	out      Validators
	declared map[xml.Name]string // root element → the XSD that declares it
	ruleSets []schematron.RuleSet
	loaded   []loadedFile
}

type loadedFile struct {
	name  string
	roots []string
}

func (l *loader) ignore(name, reason string) {
	if l.opts.Ignored != nil {
		l.opts.Ignored(name, reason)
	}
}

// add loads one directory entry, or ignores it.
func (l *loader) add(name string) error {
	// Stat follows a symbolic link, such as a Kubernetes ConfigMap's, as long
	// as it stays inside the directory.
	info, err := l.dir.root.Stat(name)
	if err != nil {
		return l.dir.errorf(name, "%w", err)
	}
	switch {
	case info.IsDir():
		l.ignore(name, "subdirectory")
		return nil
	case !info.Mode().IsRegular():
		l.ignore(name, "not a regular file")
		return nil
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".xsd" && ext != ".sch" {
		l.ignore(name, "not an .xsd or .sch file")
		return nil
	}
	data, err := l.dir.read(name)
	if err != nil {
		return l.dir.errorf(name, "%w", err)
	}
	if ext == ".sch" {
		return l.addSchematron(name, data)
	}
	return l.addXSD(name, data)
}

func (l *loader) addSchematron(name string, data []byte) error {
	rs, err := extractSchematron(name, data)
	if err != nil {
		return l.dir.errorf(name, "%w", err)
	}
	l.ruleSets = append(l.ruleSets, rs)
	l.loaded = append(l.loaded, loadedFile{name: name})
	return nil
}

func (l *loader) addXSD(name string, data []byte) error {
	types, err := compileCustomXSD(l.dir, name, data, l.opts.Embedded)
	if err != nil {
		return err
	}
	file := loadedFile{name: name}
	for _, ct := range types {
		key := ct.Name()
		if builtin, ok := qti.LookupDocumentType(key); ok {
			return l.dir.errorf(name, "root element {%s}%s is already the built-in document type %s",
				key.Space, key.Local, builtin.Schema)
		}
		if other, ok := l.declared[key]; ok {
			return l.dir.errorf(name, "root element {%s}%s is also declared by %s", key.Space, key.Local, other)
		}
		l.declared[key] = name
		l.out.Types = append(l.out.Types, ct)
		file.roots = append(file.roots, "{"+key.Space+"}"+key.Local)
	}
	l.loaded = append(l.loaded, file)
	return nil
}

// finish compiles the .sch files together and reports what was loaded.
func (l *loader) finish() (*Validators, error) {
	if len(l.ruleSets) > 0 {
		// Precompile names the file of a rule that does not compile.
		compiled, err := schematron.Precompile(l.ruleSets...)
		if err != nil {
			return nil, fmt.Errorf("validators directory: %w", err)
		}
		engine, err := schematron.New(compiled)
		if err != nil {
			return nil, fmt.Errorf("validators directory: %w", err)
		}
		l.out.Rules = rules.Mounted(engine)
	}
	if l.opts.Loaded != nil {
		for _, f := range l.loaded {
			l.opts.Loaded(f.name, f.roots)
		}
	}
	return &l.out, nil
}

// extractSchematron reads a standalone Schematron schema.
func extractSchematron(name string, data []byte) (schematron.RuleSet, error) {
	root, err := xmldoc.DetectRoot(bytes.NewReader(data))
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
func compileCustomXSD(dir validatorsDir, name string, data []byte, embedded xsdschema.Resolver) ([]Type, error) {
	roots, err := globalElements(data)
	if err != nil {
		return nil, dir.errorf(name, "%w", err)
	}
	src := xsd.Bytes(name, data).WithResolver(mountedResolver{dir: dir, embedded: embedded})
	checker, err := xsdschema.Compile(src)
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
	var embeddedRules *rules.Checker
	if len(compiled.Sets) > 0 {
		engine, err := schematron.New(compiled)
		if err != nil {
			return nil, dir.errorf(name, "%w", err)
		}
		embeddedRules = rules.Mounted(engine)
	}
	types := make([]Type, len(roots))
	for i, root := range roots {
		types[i] = Type{
			DocumentType: qti.DocumentType{Namespace: root.Space, Root: root.Local, Schema: root.Local},
			File:         name,
			Schema:       checker.WithSource(name),
			Rules:        embeddedRules,
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
				namespace = xmldoc.Attr(t, "targetNamespace")
			case depth == 2 && t.Name == xml.Name{Space: namespaceXSD, Local: "element"}:
				names = append(names, xml.Name{Space: namespace, Local: xmldoc.Attr(t, "name")})
			}
		case xml.EndElement:
			depth--
		}
	}
}

// mountedResolver resolves the includes and imports of a mounted XSD. A
// relative location names a file next to it in the validators directory; an
// https://purl.imsglobal.org/ URL, and any location inside such a schema, an
// embedded schema. Anything else fails: nothing is read from elsewhere on
// disk or fetched over the network.
type mountedResolver struct {
	dir      validatorsDir
	embedded xsdschema.Resolver
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
		return r.embedded.Source(ref.String())
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
