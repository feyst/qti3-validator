package validator

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "package", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	return []entry{
		{"imsmanifest.xml", read("imsmanifest.xml")},
		{"test.xml", read("test.xml")},
		{"items/item-1.xml", read("item-1.xml")},
		{"items/media/", ""},
		{"items/media/picture.png", "\x89PNG not really"},
	}
}

func validatePackageWith(t testing.TB, data []byte, opts ValidateOptions) PackageValidationResult {
	t.Helper()
	return testValidator(t).ValidatePackage(context.Background(), bytes.NewReader(data), int64(len(data)), PackageLimits{}, opts)
}

func validatePackage(t testing.TB, data []byte, limits PackageLimits) PackageValidationResult {
	t.Helper()
	return testValidator(t).ValidatePackage(context.Background(), bytes.NewReader(data), int64(len(data)), limits, ValidateOptions{})
}

func fileResult(t *testing.T, res PackageValidationResult, name string) FileResult {
	t.Helper()
	for _, f := range res.Files {
		if f.File == name {
			return f
		}
	}
	t.Fatalf("no result for %s in %+v", name, res.Files)
	return FileResult{}
}

func TestValidPackage(t *testing.T) {
	res := validatePackage(t, makeZip(t, packageEntries(t)...), PackageLimits{})
	if !res.Valid || res.Outcome != OutcomeValid || len(res.Errors) != 0 {
		t.Fatalf("got %+v", res)
	}
	if len(res.Files) != 3 {
		t.Fatalf("want results for the 3 XML files only, got %+v", res.Files)
	}
	if f := fileResult(t, res, "imsmanifest.xml"); f.Schema != "imscp-manifest" {
		t.Fatalf("manifest: %+v", f)
	}
	if f := fileResult(t, res, "test.xml"); f.Schema != "qti-assessment-test" {
		t.Fatalf("test: %+v", f)
	}
}

func TestPackageWithInvalidXML(t *testing.T) {
	entries := append(packageEntries(t),
		entry{"items/broken.xml", `<qti-assessment-item xmlns="` + NamespaceASI + `"><oops></qti-assessment-item>`},
		entry{"items/invalid.xml", strings.Replace(packageEntries(t)[2].data, `shuffle="false"`, `shuffle="maybe"`, 1)},
	)
	res := validatePackage(t, makeZip(t, entries...), PackageLimits{})
	if res.Valid || res.Outcome != OutcomeInvalid {
		t.Fatalf("got %+v", res)
	}
	if f := fileResult(t, res, "items/broken.xml"); f.Valid || f.Errors[0].Code != CodeInvalidXML || f.Errors[0].File != "items/broken.xml" {
		t.Fatalf("broken: %+v", f)
	}
	if f := fileResult(t, res, "items/invalid.xml"); f.Valid || f.Errors[0].Code != CodeValidation {
		t.Fatalf("invalid: %+v", f)
	}
	if f := fileResult(t, res, "items/item-1.xml"); !f.Valid {
		t.Fatalf("item-1: %+v", f)
	}
}

func TestPackageSkipsNonQTIXML(t *testing.T) {
	entries := append(packageEntries(t), entry{"extra/config.xml", `<config/>`})
	res := validatePackage(t, makeZip(t, entries...), PackageLimits{})
	if !res.Valid {
		t.Fatalf("got %+v", res)
	}
	if f := fileResult(t, res, "extra/config.xml"); !f.Skipped {
		t.Fatalf("want skipped, got %+v", f)
	}
}

func TestPackageWithoutManifest(t *testing.T) {
	res := validatePackage(t, makeZip(t, packageEntries(t)[1:]...), PackageLimits{})
	if res.Valid || res.Errors[0].Code != CodeMissingManifest {
		t.Fatalf("got %+v", res)
	}
}

func TestPackagePathTraversal(t *testing.T) {
	for _, name := range []string{"../evil.xml", "items/../../evil.xml", "/etc/evil.xml", `..\evil.xml`, "C:/evil.xml"} {
		t.Run(name, func(t *testing.T) {
			entries := append(packageEntries(t), entry{name, `<x/>`})
			res := validatePackage(t, makeZip(t, entries...), PackageLimits{})
			if res.Valid || len(res.Errors) == 0 || res.Errors[0].Code != CodeUnsafePath || res.Errors[0].File != name {
				t.Fatalf("got %+v", res)
			}
			for _, f := range res.Files {
				if f.File == name {
					t.Fatalf("unsafe entry was read: %+v", f)
				}
			}
		})
	}
}

func TestPackageNotAZip(t *testing.T) {
	res := validatePackage(t, []byte("this is not a zip"), PackageLimits{})
	if res.Outcome != OutcomeMalformed || res.Errors[0].Code != CodeInvalidZIP {
		t.Fatalf("got %+v", res)
	}
}

func TestPackageTooManyFiles(t *testing.T) {
	res := validatePackage(t, makeZip(t, packageEntries(t)...), PackageLimits{MaxFiles: 2})
	if res.Outcome != OutcomeTooLarge || res.Errors[0].Code != CodeTooManyFiles {
		t.Fatalf("got %+v", res)
	}
}

func TestPackageOversizedEntry(t *testing.T) {
	big := `<qti-assessment-item xmlns="` + NamespaceASI + `">` + strings.Repeat(" ", 1<<20) + `</qti-assessment-item>`
	entries := append(packageEntries(t), entry{"big.xml", big})
	res := validatePackage(t, makeZip(t, entries...), PackageLimits{MaxFileSize: 64 << 10})
	if res.Valid || res.Outcome != OutcomeInvalid {
		t.Fatalf("got %+v", res)
	}
	if f := fileResult(t, res, "big.xml"); f.Errors[0].Code != CodeTooLarge {
		t.Fatalf("big.xml: %+v", f)
	}
	if f := fileResult(t, res, "items/item-1.xml"); !f.Valid {
		t.Fatalf("other files must still be validated: %+v", f)
	}
}

// A ZIP bomb whose header claims a tiny size: the limit must hold on the
// bytes actually inflated, not on the header.
func TestPackageZipBombWithLyingHeader(t *testing.T) {
	payload := []byte(`<qti-assessment-item xmlns="` + NamespaceASI + `">` + strings.Repeat("A", 50<<20) + `</qti-assessment-item>`)
	var compressed bytes.Buffer
	fw, _ := flate.NewWriter(&compressed, flate.BestCompression)
	fw.Write(payload)
	fw.Close()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range packageEntries(t) {
		w, _ := zw.Create(e.name)
		w.Write([]byte(e.data))
	}
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name:               "bomb.xml",
		Method:             zip.Deflate,
		CRC32:              crc32.ChecksumIEEE(payload),
		CompressedSize64:   uint64(compressed.Len()),
		UncompressedSize64: 100, // lies
	})
	if err != nil {
		t.Fatal(err)
	}
	w.Write(compressed.Bytes())
	zw.Close()
	if buf.Len() > 1<<20 {
		t.Fatalf("bomb should be small, is %d bytes", buf.Len())
	}

	res := validatePackage(t, buf.Bytes(), PackageLimits{MaxFileSize: 1 << 20})
	f := fileResult(t, res, "bomb.xml")
	if f.Valid || f.Errors[0].Code != CodeInvalidZIP {
		t.Fatalf("bomb.xml: %+v", f)
	}
}

func TestPackageUncompressedBudget(t *testing.T) {
	entries := packageEntries(t)
	for i := range 20 {
		entries = append(entries, entry{"items/copy-" + string(rune('a'+i)) + ".xml", entries[2].data})
	}
	res := validatePackage(t, makeZip(t, entries...), PackageLimits{MaxUncompressedSize: 8 << 10})
	if res.Outcome != OutcomeTooLarge || res.Errors[len(res.Errors)-1].Code != CodeTooLarge {
		t.Fatalf("got %+v", res)
	}
}

func TestSafeEntryName(t *testing.T) {
	for name, want := range map[string]bool{
		"imsmanifest.xml": true, "items/a.xml": true, "a..b.xml": true,
		"": false, "../a.xml": false, "a/../../b": false, "/a.xml": false, `a\b.xml`: false, "c:/a.xml": false, "a\x00.xml": false,
	} {
		if got := safeEntryName(name); got != want {
			t.Errorf("safeEntryName(%q) = %v, want %v", name, got, want)
		}
	}
}

// A ZIP bomb with an honest header is refused before inflating.
func TestPackageZipBombWithHonestHeader(t *testing.T) {
	entries := append(packageEntries(t), entry{"bomb.xml", strings.Repeat("A", 20<<20)})
	data := makeZip(t, entries...)
	if len(data) > 1<<20 {
		t.Fatalf("bomb should be small, is %d bytes", len(data))
	}
	res := validatePackage(t, data, PackageLimits{MaxFileSize: 1 << 20})
	if f := fileResult(t, res, "bomb.xml"); f.Valid || f.Errors[0].Code != CodeTooLarge {
		t.Fatalf("bomb.xml: %+v", f)
	}
}

// A file the manifest lists as a QTI item must be a QTI document; one in
// another namespace (here QTI 2.2) is an error, not a skipped file.
func TestPackageQTIResourceInWrongNamespace(t *testing.T) {
	entries := packageEntries(t)
	entries[2].data = strings.Replace(entries[2].data, NamespaceASI, "http://www.imsglobal.org/xsd/imsqti_v2p2", 1)
	res := validatePackageWith(t, makeZip(t, entries...), ValidateOptions{})
	f := fileResult(t, res, "items/item-1.xml")
	if res.Valid || f.Skipped || f.Valid || f.Errors[0].Code != CodeUnsupportedDocument {
		t.Fatalf("got %+v", f)
	}
	// The same XML outside the manifest's QTI resources is only skipped.
	entries = append(packageEntries(t), entry{"extra/old.xml", strings.Replace(entries[2].data, "items", "x", 0)})
	res = validatePackageWith(t, makeZip(t, entries...), ValidateOptions{})
	if f := fileResult(t, res, "extra/old.xml"); !f.Skipped || !res.Valid {
		t.Fatalf("got %+v", f)
	}
}
