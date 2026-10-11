package ziparchive

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"runtime"
	"testing"

	"qti3-validator/internal/app"
	"qti3-validator/internal/testutil"
)

func TestOpen(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("a/b.xml")
	_, _ = w.Write([]byte("<x/>"))
	_, _ = zw.Create("a/")
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()), 2)
	if err != nil {
		t.Fatal(err)
	}
	entries := a.Entries()
	if len(entries) != 2 || entries[0].Name() != "a/b.xml" || entries[0].IsDir() || entries[0].DeclaredSize() != 4 || !entries[1].IsDir() {
		t.Fatalf("entries %+v", entries)
	}
	rc, err := entries[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if data, _ := io.ReadAll(rc); string(data) != "<x/>" {
		t.Fatalf("data %q", data)
	}
	if _, err := Open(bytes.NewReader([]byte("not a zip")), 9, 2); err == nil {
		t.Fatal("opened a non-ZIP")
	}
}

// An archive with more entries than allowed is refused before its central
// directory is read into memory: archive/zip keeps a File for every record,
// about 280 bytes each, for records of 47 bytes.
func TestOpenTooManyEntries(t *testing.T) {
	const records = (100<<20 - 200) / 47 // a ZIP just under MAX_PACKAGE_SIZE
	for _, tc := range []struct {
		name     string
		declared int
		zip64    bool
	}{
		{"zip64", records, true},
		// archive/zip reads records until a bad one, whatever the end of the
		// central directory declares.
		{"zip64 declaring one entry", 1, true},
		{"declaring the count modulo 65536", records % 65536, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := testutil.CrowdedZIP(records, tc.declared, tc.zip64)
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			_, err := Open(bytes.NewReader(data), int64(len(data)), 1000)
			runtime.ReadMemStats(&after)
			if !errors.Is(err, app.ErrTooManyEntries) {
				t.Fatalf("got %v, want ErrTooManyEntries", err)
			}
			if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 10<<20 {
				t.Fatalf("allocated %d MiB to refuse the archive", alloc>>20)
			}
		})
	}
}

func TestOpenEntryLimit(t *testing.T) {
	for _, zip64 := range []bool{false, true} {
		data := testutil.CrowdedZIP(1000, 1000, zip64)
		if a, err := Open(bytes.NewReader(data), int64(len(data)), 1000); err != nil || len(a.Entries()) != 1000 {
			t.Fatalf("zip64 %v: 1000 entries at the limit: %v", zip64, err)
		}
		data = testutil.CrowdedZIP(1001, 1001, zip64)
		if _, err := Open(bytes.NewReader(data), int64(len(data)), 1000); !errors.Is(err, app.ErrTooManyEntries) {
			t.Fatalf("zip64 %v: 1001 entries: %v", zip64, err)
		}
	}
}

// A damaged end of central directory is an invalid ZIP, not too many
// entries.
func TestOpenDamagedEnd(t *testing.T) {
	data := testutil.CrowdedZIP(5000, 5000, true)
	end := len(data) - 22
	for name, damaged := range map[string][]byte{
		"truncated end record": data[:len(data)-10],
		"no end record":        data[:end],
		"bad zip64 locator offset": func() []byte {
			d := bytes.Clone(data)
			copy(d[end-12:], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f})
			return d
		}(),
		"bad zip64 record": func() []byte {
			d := bytes.Clone(data)
			copy(d[end-20-56:], "XXXX")
			return d
		}(),
		"garbage":   bytes.Repeat([]byte("PK\x05\x06"), 100),
		"empty":     {},
		"end alone": []byte("PK\x05\x06"),
	} {
		_, err := Open(bytes.NewReader(damaged), int64(len(damaged)), 1000)
		if err == nil || errors.Is(err, app.ErrTooManyEntries) {
			t.Errorf("%s: got %v, want a format error", name, err)
		}
	}
}
