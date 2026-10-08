// Package schemastore serves the schemas and Schematron rules embedded at
// build time. It never touches the network: a schema without a local file is
// reported as missing.
//
// The schemas are fetched by cmd/fetchschemas, pinned by schemas/schemas.lock.
// Each file lives gzip-compressed under its URL host and path plus ".gz", so
// the absolute schemaLocation URLs in the XSDs map directly to files. The
// same command compiles the Schematron rules embedded in the XSDs, and the
// validator's own rules, to schemas/schematron.json.gz.
package schemastore

import (
	"bytes"
	"compress/gzip"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path"

	"qti3-validator/internal/lib/schematron"
	"qti3-validator/internal/lib/xmlenc"
)

//go:embed schemas/purl.imsglobal.org schemas/schematron.json.gz
var embedded embed.FS

// RulesFile is the compiled Schematron rules, relative to the schema tree.
const RulesFile = "schematron.json.gz"

// Store reads schemas from a file system laid out as URL host and path.
type Store struct {
	fsys fs.FS
}

// Embedded returns the store of the embedded schemas.
func Embedded() Store {
	sub, err := fs.Sub(embedded, "schemas")
	if err != nil {
		panic(err) // the embed pattern guarantees the directory exists
	}
	return Store{fsys: sub}
}

// New returns a store over fsys, for tests.
func New(fsys fs.FS) Store { return Store{fsys: fsys} }

// FS returns the store's file system.
func (s Store) FS() fs.FS { return s.fsys }

// Has reports whether the schema at schemaURL is stored.
func (s Store) Has(schemaURL string) error {
	name, err := localPath(schemaURL)
	if err != nil {
		return err
	}
	if _, err := fs.Stat(s.fsys, name+".gz"); err != nil {
		return fmt.Errorf("schema %s is not embedded: %w", schemaURL, err)
	}
	return nil
}

// Open returns the schema at schemaURL as UTF-8. Schemas the XSD library
// cannot compile are served from overrides instead.
func (s Store) Open(schemaURL string) (io.ReadCloser, error) {
	if override, ok := overrides[schemaURL]; ok {
		return io.NopCloser(bytes.NewReader([]byte(override))), nil
	}
	name, err := localPath(schemaURL)
	if err != nil {
		return nil, err
	}
	return openUTF8(s.fsys, name+".gz")
}

// Rules returns the Schematron rules compiled at build time.
func (s Store) Rules() (*schematron.Compiled, error) {
	f, err := s.fsys.Open(RulesFile)
	if err != nil {
		return nil, fmt.Errorf("schematron rules: %w", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("schematron rules: %w", err)
	}
	var compiled schematron.Compiled
	if err := json.NewDecoder(zr).Decode(&compiled); err != nil {
		return nil, fmt.Errorf("schematron rules: %w", err)
	}
	return &compiled, nil
}

// localPath maps https://host/path to host/path.
func localPath(schemaURL string) (string, error) {
	u, err := url.Parse(schemaURL)
	if err != nil {
		return "", err
	}
	if u.Scheme != "https" && u.Scheme != "http" || u.Host == "" {
		return "", fmt.Errorf("schema location %q is not an absolute URL", schemaURL)
	}
	name := path.Clean(u.Host + u.Path)
	if !fs.ValidPath(name) {
		return "", fmt.Errorf("schema location %q does not map to a local file", schemaURL)
	}
	return name, nil
}

// openUTF8 opens a gzip-compressed schema file, transcoding UTF-16 to UTF-8.
// The XSD library accepts only UTF-8, and the upstream XInclude.xsd is UTF-16
// with a BOM. Transcoding at load time keeps the pinned files byte-identical
// to upstream.
func openUTF8(fsys fs.FS, name string) (io.ReadCloser, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if data, err = xmlenc.ToUTF8(data); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
