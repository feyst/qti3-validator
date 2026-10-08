package app_test

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"qti3-validator/internal/app"
	"qti3-validator/internal/bootstrap"
	"qti3-validator/internal/domain/qti"
	"qti3-validator/internal/testutil"
)

var (
	sharedOnce      sync.Once
	sharedValidator *app.Validator
	sharedErr       error
)

// testValidator compiles the embedded schemas once for the whole package.
func testValidator(t testing.TB) *app.Validator {
	t.Helper()
	sharedOnce.Do(func() { sharedValidator, sharedErr = bootstrap.NewValidator(bootstrap.Options{}) })
	if sharedErr != nil {
		t.Fatal(sharedErr)
	}
	return sharedValidator
}

func readTestdata(t testing.TB, name string) string {
	t.Helper()
	return string(testutil.ReadTestdata(t, name))
}

func validateFile(t *testing.T, v *app.Validator, name string) qti.DocumentResult {
	t.Helper()
	f, err := os.Open(testutil.Testdata(name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	return v.ValidateDocument(context.Background(), app.ValidateDocument{Document: f})
}

// validateString validates doc, forcing version unless it is empty.
func validateString(t *testing.T, doc, version string) qti.DocumentResult {
	t.Helper()
	return testValidator(t).ValidateDocument(context.Background(), app.ValidateDocument{Document: strings.NewReader(doc), Version: version})
}

type entry struct {
	name string
	data string
}

func makeZip(t testing.TB, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func packageEntries(t testing.TB) []entry {
	t.Helper()
	read := func(name string) string { return readTestdata(t, "package/"+name) }
	return []entry{
		{"imsmanifest.xml", read("imsmanifest.xml")},
		{"test.xml", read("test.xml")},
		{"items/item-1.xml", read("item-1.xml")},
		{"items/media/", ""},
		{"items/media/picture.png", "\x89PNG not really"},
	}
}

// validatePackageWith validates a package, forcing version unless it is empty.
func validatePackageWith(t testing.TB, data []byte, version string) qti.PackageResult {
	t.Helper()
	return testValidator(t).ValidatePackage(context.Background(), app.ValidatePackage{
		Package: bytes.NewReader(data), Size: int64(len(data)), Version: version,
	})
}

func validatePackage(t testing.TB, data []byte, limits qti.PackageLimits) qti.PackageResult {
	t.Helper()
	return testValidator(t).ValidatePackage(context.Background(), app.ValidatePackage{
		Package: bytes.NewReader(data), Size: int64(len(data)), Limits: limits,
	})
}

func fileResult(t *testing.T, res qti.PackageResult, name string) qti.FileResult {
	t.Helper()
	for _, f := range res.Files {
		if f.File == name {
			return f
		}
	}
	t.Fatalf("no result for %s in %+v", name, res.Files)
	return qti.FileResult{}
}
