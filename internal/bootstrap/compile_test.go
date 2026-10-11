package bootstrap

import (
	"bytes"
	"io/fs"
	"math"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"qti3-validator/internal/adapter/schemastore"
	"qti3-validator/internal/adapter/xsdschema"
	"qti3-validator/internal/domain/qti"
	"qti3-validator/internal/lib/xsdelements"
	"qti3-validator/internal/testutil"
)

// embeddedCopy returns a store over a copy of the embedded schemas, which a
// test may change.
func embeddedCopy(t *testing.T) (schemastore.Store, fstest.MapFS) {
	t.Helper()
	embedded := schemastore.Embedded().FS()
	fsys := fstest.MapFS{}
	err := fs.WalkDir(embedded, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(embedded, p)
		fsys[p] = &fstest.MapFile{Data: data}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return schemastore.New(fsys), fsys
}

// The element names come from the file the build made of them. The service
// does not start without them, and the check of the image build, which
// reads the schemas again, fails on names that are not those of the schemas.
func TestElementsAreLoadedAndVerified(t *testing.T) {
	if _, err := NewValidator(Options{VerifyElements: true}); err != nil {
		t.Fatalf("the embedded elements are not those of the embedded schemas: %v", err)
	}
	asi := qti.LatestVersion().ASI
	write := func(fsys fstest.MapFS, c *xsdelements.Compiled) {
		var buf bytes.Buffer
		if err := c.Encode(&buf); err != nil {
			t.Fatal(err)
		}
		fsys[schemastore.ElementsFile] = &fstest.MapFile{Data: buf.Bytes()}
	}

	t.Run("stale", func(t *testing.T) {
		store, fsys := embeddedCopy(t)
		c, err := store.Elements()
		if err != nil {
			t.Fatal(err)
		}
		schema := c.Schemas[asi]
		schema.ManyTyped = nil
		c.Schemas[asi] = schema
		write(fsys, c)
		// The start does not read the schemas a second time, so it is the
		// check that finds it.
		if _, err := NewValidator(Options{Store: &store}); err != nil {
			t.Errorf("the start read the element names again: %v", err)
		}
		_, err = NewValidator(Options{Store: &store, VerifyElements: true})
		if err == nil || !strings.Contains(err.Error(), asi) || !strings.Contains(err.Error(), "fetchschemas") {
			t.Errorf("got %v, want an error that names the schema and says to run fetchschemas", err)
		}
	})
	t.Run("a version without elements", func(t *testing.T) {
		store, fsys := embeddedCopy(t)
		c, err := store.Elements()
		if err != nil {
			t.Fatal(err)
		}
		delete(c.Schemas, asi)
		write(fsys, c)
		if _, err := NewValidator(Options{Store: &store}); err == nil || !strings.Contains(err.Error(), asi) {
			t.Errorf("got %v, want an error that names the schema", err)
		}
	})
	t.Run("another format version", func(t *testing.T) {
		store, fsys := embeddedCopy(t)
		write(fsys, &xsdelements.Compiled{Version: xsdelements.CompiledVersion + 1})
		if _, err := NewValidator(Options{Store: &store}); err == nil || !strings.Contains(err.Error(), "format version") {
			t.Errorf("got %v, want the format version refused", err)
		}
	})
	t.Run("no file", func(t *testing.T) {
		store, fsys := embeddedCopy(t)
		delete(fsys, schemastore.ElementsFile)
		if _, err := NewValidator(Options{Store: &store}); err == nil || !strings.Contains(err.Error(), schemastore.ElementsFile) {
			t.Errorf("got %v, want an error that names the file", err)
		}
	})
}

// The QTI versions compile at the same time or one after the other, with
// the same result: the schemas in the order of the versions, or the error of
// the first one that fails.
func TestCompileVersions(t *testing.T) {
	item := testutil.ReadTestdata(t, "valid/assessment-item.xml")
	versions := qti.Versions()
	broken, fsys := embeddedCopy(t)
	delete(fsys, "purl.imsglobal.org/spec/mathml/v3p0/schema/xsd/mathml3-common.xsd.gz")
	for name, together := range map[string]bool{"together": true, "one after the other": false} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			loaded := map[string]int{}
			resolver := xsdschema.Resolver{Store: schemastore.Embedded(), Loaded: func(u string) {
				mu.Lock()
				defer mu.Unlock()
				loaded[u]++
			}}
			schemas, err := compileVersions(resolver, versions, together)
			if err != nil {
				t.Fatal(err)
			}
			if len(schemas) != len(versions) {
				t.Fatalf("%d schemas for %d versions", len(schemas), len(versions))
			}
			for i, v := range versions {
				if verdict := schemas[i].CheckSchema(item, qti.DefaultLimits); verdict.Outcome != qti.OutcomeValid {
					t.Errorf("QTI %s: a valid item is %v: %+v", v.Name, verdict.Outcome, verdict.Findings)
				}
				// Each version loaded its own schema, once.
				if loaded[v.ASI] != 1 {
					t.Errorf("QTI %s: its schema was loaded %d times", v.Name, loaded[v.ASI])
				}
			}

			_, err = compileVersions(xsdschema.Resolver{Store: broken}, versions, together)
			if err == nil || !strings.Contains(err.Error(), "mathml3-common.xsd") || !strings.Contains(err.Error(), "QTI "+versions[0].Name) {
				t.Errorf("got %v, want the error of the first version, naming the missing schema", err)
			}
		})
	}
}

func TestRoomToCompileTogether(t *testing.T) {
	const mib = 1 << 20
	for _, tt := range []struct {
		cpus  int
		limit int64
		want  bool
	}{
		{2, math.MaxInt64, true}, // no soft limit
		{8, 224 * mib, true},
		{2, 80 * mib, true}, // the limit of the service while it starts
		{2, minMemoryToCompileTogether, true},
		{2, minMemoryToCompileTogether - 1, false},
		{8, 32 * mib, false},
		{1, math.MaxInt64, false},
		{1, 70 * mib, false},
	} {
		if got := roomToCompileTogether(tt.cpus, tt.limit); got != tt.want {
			t.Errorf("%d CPUs with a soft limit of %d MiB: %v, want %v", tt.cpus, tt.limit/mib, got, tt.want)
		}
	}
}
