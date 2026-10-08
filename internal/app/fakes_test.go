package app

import (
	"bytes"
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
}

func (f *fakeSchema) CheckSchema([]byte, qti.Limits) SchemaVerdict {
	f.checked++
	return f.verdict
}

// fakeRules returns fixed findings, or an error.
type fakeRules struct {
	findings []RuleFinding
	err      error
	max      int // the maxFindings it was called with
	checked  int
}

func (f *fakeRules) CheckRules(_ []byte, maxFindings int) ([]RuleFinding, error) {
	f.checked++
	f.max = maxFindings
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
	if e.readErr != nil {
		r = io.MultiReader(r, errReader{e.readErr})
	}
	return io.NopCloser(r), nil
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// newTestValidator returns a validator whose every version uses schema and
// rules, and whose archive is entries.
func newTestValidator(t *testing.T, schema SchemaChecker, rules RuleChecker, entries ...ArchiveEntry) *Validator {
	t.Helper()
	cfg := Config{
		Versions: map[string]Profile{},
		OpenArchive: func(io.ReaderAt, int64) (Archive, error) {
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
