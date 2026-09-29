package bootstrap

import (
	"bufio"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kennisnet/qti3-validator/internal/adapter/schemastore"
	"github.com/kennisnet/qti3-validator/internal/testutil"
)

func TestMissingSchemaDependencyFailsAtStartup(t *testing.T) {
	const missing = "purl.imsglobal.org/spec/mathml/v3p0/schema/xsd/mathml3-common.xsd.gz"
	embedded := schemastore.Embedded().FS()
	fsys := fstest.MapFS{}
	err := fs.WalkDir(embedded, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == missing {
			return err
		}
		data, err := fs.ReadFile(embedded, p)
		fsys[p] = &fstest.MapFile{Data: data}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	store := schemastore.New(fsys)
	_, err = NewValidator(Options{Store: &store})
	if err == nil || !strings.Contains(err.Error(), "mathml3-common.xsd") {
		t.Fatalf("want an error naming the missing schema, got %v", err)
	}
}

// Every schema the compiler loads must be pinned in the lock file, and each
// is reported once.
func TestLoadedSchemasArePinned(t *testing.T) {
	f, err := os.Open(testutil.Path("internal/adapter/schemastore/schemas/schemas.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pinned := map[string]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) == 2 && !strings.HasPrefix(fields[0], "#") {
			pinned[fields[1]] = true
		}
	}
	loaded := map[string]int{}
	if _, err := NewValidator(Options{OnSchemaLoaded: func(u string) { loaded[u]++ }}); err != nil {
		t.Fatal(err)
	}
	if len(loaded) == 0 {
		t.Fatal("no schemas loaded")
	}
	for u, n := range loaded {
		if !pinned[u] {
			t.Errorf("loaded schema %s is not in schemas.lock", u)
		}
		if n != 1 {
			t.Errorf("schema %s reported %d times", u, n)
		}
	}
}
