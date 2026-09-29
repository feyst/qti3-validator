// Package testutil holds helpers shared by tests.
package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// root is the repository root, found from this file's location so tests in
// any package can use it.
var root = func() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}()

// Path returns the path of a file relative to the repository root.
func Path(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }

// Testdata returns the path of a file in testdata/.
func Testdata(name string) string { return Path("testdata/" + name) }

// ReadTestdata returns the contents of a file in testdata/.
func ReadTestdata(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(Testdata(name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
