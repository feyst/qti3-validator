package app_test

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"hash/crc32"
	"strings"
	"testing"

	"qti3-validator/internal/domain/qti"
)

func TestValidPackage(t *testing.T) {
	res := validatePackage(t, makeZip(t, packageEntries(t)...), qti.PackageLimits{})
	if !res.Valid || res.Outcome != qti.OutcomeValid || len(res.Errors) != 0 {
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
		entry{"items/broken.xml", `<qti-assessment-item xmlns="` + qti.NamespaceASI + `"><oops></qti-assessment-item>`},
		entry{"items/invalid.xml", strings.Replace(packageEntries(t)[2].data, `shuffle="false"`, `shuffle="maybe"`, 1)},
	)
	res := validatePackage(t, makeZip(t, entries...), qti.PackageLimits{})
	if res.Valid || res.Outcome != qti.OutcomeInvalid {
		t.Fatalf("got %+v", res)
	}
	if f := fileResult(t, res, "items/broken.xml"); f.Valid || f.Errors[0].Code != qti.CodeInvalidXML || f.Errors[0].File != "items/broken.xml" {
		t.Fatalf("broken: %+v", f)
	}
	if f := fileResult(t, res, "items/invalid.xml"); f.Valid || f.Errors[0].Code != qti.CodeValidation {
		t.Fatalf("invalid: %+v", f)
	}
	if f := fileResult(t, res, "items/item-1.xml"); !f.Valid {
		t.Fatalf("item-1: %+v", f)
	}
}

func TestPackageSkipsNonQTIXML(t *testing.T) {
	entries := append(packageEntries(t), entry{"extra/config.xml", `<config/>`})
	res := validatePackage(t, makeZip(t, entries...), qti.PackageLimits{})
	if !res.Valid {
		t.Fatalf("got %+v", res)
	}
	if f := fileResult(t, res, "extra/config.xml"); !f.Skipped {
		t.Fatalf("want skipped, got %+v", f)
	}
}

func TestPackageWithoutManifest(t *testing.T) {
	res := validatePackage(t, makeZip(t, packageEntries(t)[1:]...), qti.PackageLimits{})
	if res.Valid || res.Errors[0].Code != qti.CodeMissingManifest {
		t.Fatalf("got %+v", res)
	}
}

func TestPackagePathTraversal(t *testing.T) {
	for _, name := range []string{"../evil.xml", "items/../../evil.xml", "/etc/evil.xml", `..\evil.xml`, "C:/evil.xml"} {
		t.Run(name, func(t *testing.T) {
			entries := append(packageEntries(t), entry{name, `<x/>`})
			res := validatePackage(t, makeZip(t, entries...), qti.PackageLimits{})
			if res.Valid || len(res.Errors) == 0 || res.Errors[0].Code != qti.CodeUnsafePath || res.Errors[0].File != name {
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
	res := validatePackage(t, []byte("this is not a zip"), qti.PackageLimits{})
	if res.Outcome != qti.OutcomeMalformed || res.Errors[0].Code != qti.CodeInvalidZIP {
		t.Fatalf("got %+v", res)
	}
}

func TestPackageTooManyFiles(t *testing.T) {
	res := validatePackage(t, makeZip(t, packageEntries(t)...), qti.PackageLimits{MaxFiles: 2})
	if res.Outcome != qti.OutcomeTooLarge || res.Errors[0].Code != qti.CodeTooManyFiles {
		t.Fatalf("got %+v", res)
	}
}

func TestPackageOversizedEntry(t *testing.T) {
	big := `<qti-assessment-item xmlns="` + qti.NamespaceASI + `">` + strings.Repeat(" ", 1<<20) + `</qti-assessment-item>`
	entries := append(packageEntries(t), entry{"big.xml", big})
	res := validatePackage(t, makeZip(t, entries...), qti.PackageLimits{MaxFileSize: 64 << 10})
	if res.Valid || res.Outcome != qti.OutcomeInvalid {
		t.Fatalf("got %+v", res)
	}
	if f := fileResult(t, res, "big.xml"); f.Errors[0].Code != qti.CodeTooLarge {
		t.Fatalf("big.xml: %+v", f)
	}
	if f := fileResult(t, res, "items/item-1.xml"); !f.Valid {
		t.Fatalf("other files must still be validated: %+v", f)
	}
}

// A ZIP bomb whose header claims a tiny size: the limit must hold on the
// bytes actually inflated, not on the header.
func TestPackageZipBombWithLyingHeader(t *testing.T) {
	payload := []byte(`<qti-assessment-item xmlns="` + qti.NamespaceASI + `">` + strings.Repeat("A", 50<<20) + `</qti-assessment-item>`)
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

	res := validatePackage(t, buf.Bytes(), qti.PackageLimits{MaxFileSize: 1 << 20})
	f := fileResult(t, res, "bomb.xml")
	if f.Valid || f.Errors[0].Code != qti.CodeInvalidZIP {
		t.Fatalf("bomb.xml: %+v", f)
	}
}

func TestPackageUncompressedBudget(t *testing.T) {
	entries := packageEntries(t)
	for i := range 20 {
		entries = append(entries, entry{"items/copy-" + string(rune('a'+i)) + ".xml", entries[2].data})
	}
	res := validatePackage(t, makeZip(t, entries...), qti.PackageLimits{MaxUncompressedSize: 8 << 10})
	if res.Outcome != qti.OutcomeTooLarge || res.Errors[len(res.Errors)-1].Code != qti.CodeTooLarge {
		t.Fatalf("got %+v", res)
	}
}

// A ZIP bomb with an honest header is refused before inflating.
func TestPackageZipBombWithHonestHeader(t *testing.T) {
	entries := append(packageEntries(t), entry{"bomb.xml", strings.Repeat("A", 20<<20)})
	data := makeZip(t, entries...)
	if len(data) > 1<<20 {
		t.Fatalf("bomb should be small, is %d bytes", len(data))
	}
	res := validatePackage(t, data, qti.PackageLimits{MaxFileSize: 1 << 20})
	if f := fileResult(t, res, "bomb.xml"); f.Valid || f.Errors[0].Code != qti.CodeTooLarge {
		t.Fatalf("bomb.xml: %+v", f)
	}
}

// A file the manifest lists as a QTI item must be a QTI document; one in
// another namespace (here QTI 2.2) is an error, not a skipped file.
func TestPackageQTIResourceInWrongNamespace(t *testing.T) {
	entries := packageEntries(t)
	entries[2].data = strings.Replace(entries[2].data, qti.NamespaceASI, "http://www.imsglobal.org/xsd/imsqti_v2p2", 1)
	res := validatePackageWith(t, makeZip(t, entries...), "")
	f := fileResult(t, res, "items/item-1.xml")
	if res.Valid || f.Skipped || f.Valid || f.Errors[0].Code != qti.CodeUnsupportedDocument {
		t.Fatalf("got %+v", f)
	}
	// The same XML outside the manifest's QTI resources is only skipped.
	entries = append(packageEntries(t), entry{"extra/old.xml", entries[2].data})
	res = validatePackageWith(t, makeZip(t, entries...), "")
	if f := fileResult(t, res, "extra/old.xml"); !f.Skipped || !res.Valid {
		t.Fatalf("got %+v", f)
	}
}
