package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"qti3-validator/internal/domain/qti"
)

// fakeSchema returns a fixed verdict and records what it checked.
type fakeSchema struct {
	verdict SchemaVerdict
	checked int
	during  func() // called while checking
}

func (f *fakeSchema) CheckSchema([]byte, qti.Limits) SchemaVerdict {
	f.checked++
	if f.during != nil {
		f.during()
	}
	return f.verdict
}

// fakeRules returns fixed findings, or an error.
type fakeRules struct {
	findings []RuleFinding
	err      error
	limits   RuleLimits // the limits it was called with
	checked  int
}

func (f *fakeRules) CheckRules(_ context.Context, _ []byte, limits RuleLimits) ([]RuleFinding, error) {
	f.checked++
	f.limits = limits
	return f.findings, f.err
}

var validVerdict = SchemaVerdict{Outcome: qti.OutcomeValid, Conclusive: true}

// fakeArchive is an archive of in-memory entries.
type fakeArchive []ArchiveEntry

func (a fakeArchive) Entries() []ArchiveEntry { return a }

type fakeEntry struct {
	name     string
	data     string
	declared uint64 // declared size; len(data) when zero
	readErr  error  // returned after the data
	during   func() // called halfway through the data
}

func (e fakeEntry) Name() string { return e.name }
func (e fakeEntry) IsDir() bool  { return strings.HasSuffix(e.name, "/") }
func (e fakeEntry) DeclaredSize() uint64 {
	if e.declared != 0 {
		return e.declared
	}
	return uint64(len(e.data))
}

func (e fakeEntry) Open() (io.ReadCloser, error) {
	r := io.Reader(strings.NewReader(e.data))
	if e.during != nil {
		half := len(e.data) / 2
		r = io.MultiReader(strings.NewReader(e.data[:half]), hookReader(e.during), strings.NewReader(e.data[half:]))
	}
	if e.readErr != nil {
		r = io.MultiReader(r, errReader{e.readErr})
	}
	return io.NopCloser(r), nil
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// hookReader calls itself and reads nothing.
type hookReader func()

func (h hookReader) Read([]byte) (int, error) {
	h()
	return 0, io.EOF
}

// newTestValidator returns a validator whose every version uses schema and
// rules, and whose archive is entries.
func newTestValidator(t *testing.T, schema SchemaChecker, rules RuleChecker, entries ...ArchiveEntry) *Validator {
	t.Helper()
	cfg := Config{
		Versions: map[string]Profile{},
		OpenArchive: func(io.ReaderAt, int64, int) (Archive, error) {
			if entries == nil {
				return nil, errors.New("not a zip")
			}
			return fakeArchive(entries), nil
		},
	}
	for _, name := range qti.SupportedVersions() {
		cfg.Versions[name] = Profile{Schema: schema, Rules: rules}
	}
	v, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

const item = `<qti-assessment-item xmlns="` + qti.NamespaceASI + `"/>`

func validateDoc(t *testing.T, v *Validator, doc string) qti.DocumentResult {
	t.Helper()
	return v.ValidateDocument(t.Context(), ValidateDocument{Document: strings.NewReader(doc)})
}

func manifest(version string) string {
	return `<manifest xmlns="` + qti.NamespaceManifest + `"><metadata><schemaversion>` + version +
		`</schemaversion></metadata><resources><resource type="imsqti_item_xmlv3p0" href="item.xml"/></resources></manifest>`
}

func validatePkg(t *testing.T, v *Validator, limits qti.PackageLimits) qti.PackageResult {
	t.Helper()
	return v.ValidatePackage(t.Context(), ValidatePackage{Package: bytes.NewReader(nil), Limits: limits})
}

// fakeReferences makes every item include the given files with XInclude.
type fakeReferences []string

func (f fakeReferences) ReadReferences(data []byte) (qti.DocumentReferences, error) {
	var refs qti.DocumentReferences
	if bytes.HasPrefix(data, []byte("<qti-assessment-item")) {
		for _, href := range f {
			refs.References = append(refs.References, qti.Reference{Kind: qti.RefAsset, Href: href, Element: "xi:include", Include: true})
		}
	}
	return refs, nil
}
