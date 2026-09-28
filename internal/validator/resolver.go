package validator

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path"

	"github.com/jacoelho/xsd"
	"github.com/kennisnet/qti3-validator/internal/xmlenc"
)

// resolver maps schema URLs to files in a local file system. It never touches
// the network: a URL without a local file is reported as missing.
type resolver struct {
	fsys   fs.FS
	loaded func(url string)
}

// source returns a schema source named by its absolute URL, so relative
// includes and imports resolve against that URL.
func (r resolver) source(schemaURL string) (xsd.SchemaSource, error) {
	name, err := localPath(schemaURL)
	if err != nil {
		return xsd.SchemaSource{}, err
	}
	name += ".gz"
	if _, err := fs.Stat(r.fsys, name); err != nil {
		return xsd.SchemaSource{}, fmt.Errorf("schema %s is not embedded: %w", schemaURL, err)
	}
	if r.loaded != nil {
		r.loaded(schemaURL)
	}
	if override, ok := overrides[schemaURL]; ok {
		return xsd.Bytes(schemaURL, []byte(override)), nil
	}
	return xsd.Open(schemaURL, func() (io.ReadCloser, error) {
		return openUTF8(r.fsys, name)
	}), nil
}

// ResolveSchema implements xsd.Resolver.
func (r resolver) ResolveSchema(base, location string) (xsd.SchemaSource, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return xsd.SchemaSource{}, err
	}
	ref, err := url.Parse(location)
	if err != nil {
		return xsd.SchemaSource{}, err
	}
	// A missing schema is a hard error, not xsderrors.ErrSchemaNotFound:
	// that would let compilation carry on without it and fail later on an
	// unrelated reference, or not at all.
	return r.source(baseURL.ResolveReference(ref).String())
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

// openUTF8 opens a gzip-compressed schema file, transcoding UTF-16 to UTF-8. The validator
// accepts only UTF-8, and the upstream XInclude.xsd is UTF-16 with a BOM.
// Transcoding at load time keeps the pinned files byte-identical to upstream.
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
