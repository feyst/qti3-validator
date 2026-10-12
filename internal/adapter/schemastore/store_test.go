package schemastore

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"qti3-validator/internal/testutil"
)

// Pinned returns the URLs in schemas.lock.
func pinned(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open(testutil.Path("internal/adapter/schemastore/schemas/schemas.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	urls := map[string]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) == 2 && !strings.HasPrefix(fields[0], "#") {
			urls[fields[1]] = true
		}
	}
	return urls
}

// An override must target a pinned URL.
func TestOverridesArePinned(t *testing.T) {
	urls := pinned(t)
	for u := range overrides {
		if !urls[u] {
			t.Errorf("override %s is not in schemas.lock", u)
		}
	}
}

func TestOpen(t *testing.T) {
	s := Embedded()
	for u := range pinned(t) {
		if err := s.Has(u); err != nil {
			t.Fatal(err)
		}
		rc, err := s.Open(u)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil || !strings.Contains(string(data), "schema") {
			t.Fatalf("%s: %d bytes, %v", u, len(data), err)
		}
	}
	if err := s.Has("https://example.org/missing.xsd"); err == nil {
		t.Fatal("found a schema that is not embedded")
	}
	if _, err := s.Open("file:///etc/passwd"); err == nil {
		t.Fatal("opened a non-HTTP location")
	}
	if _, err := s.Rules(); err != nil {
		t.Fatal(err)
	}
}

// gzipOf returns data as one gzip member.
func gzipOf(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The size a gzip file states at its end is a hint for the buffer the
// schema is read into, and no more than that: it is the size of the last
// member only, modulo 4 GiB, and four bytes anyone can write.
func TestOpenDoesNotTrustTheSizeInTheTrailer(t *testing.T) {
	const url = "https://example.org/x/schema.xsd"
	// open returns the schema, and the bytes allocated to open it.
	open := func(file []byte) ([]byte, uint64, error) {
		store := New(fstest.MapFS{"example.org/x/schema.xsd.gz": {Data: file}})
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		rc, err := store.Open(url)
		runtime.ReadMemStats(&after)
		allocated := after.TotalAlloc - before.TotalAlloc
		if err != nil {
			return nil, allocated, err
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		return data, allocated, err
	}
	schema := []byte(`<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">` + strings.Repeat("<!-- x -->", 5000) + `</xs:schema>`)

	data, allocated, err := open(gzipOf(t, schema))
	if err != nil || !bytes.Equal(data, schema) {
		t.Fatalf("read %d bytes, %v", len(data), err)
	}
	t.Logf("a schema of %d bytes: %d bytes allocated", len(schema), allocated)

	// A size beyond maxSizeHint is not believed: no buffer is made for it.
	// gzip then finds the size wrong, so such a file is refused, having cost
	// no more memory than its content.
	for _, stated := range []uint32{maxSizeHint + 1, 1 << 30, math.MaxUint32} {
		forged := gzipOf(t, schema)
		binary.LittleEndian.PutUint32(forged[len(forged)-4:], stated)
		_, allocated, err := open(forged)
		if !errors.Is(err, gzip.ErrChecksum) {
			t.Errorf("stated size %d: got %v, want the checksum error of gzip", stated, err)
		}
		if allocated > 8<<20 {
			t.Errorf("stated size %d: %d MiB allocated for a schema of %d bytes", stated, allocated>>20, len(schema))
		}
	}

	// A size smaller than the content: the file is two members, and the
	// trailer states the size of the last one. All of it is read.
	rest := []byte("<!-- the end -->")
	data, _, err = open(append(gzipOf(t, schema), gzipOf(t, rest)...))
	if want := append(slices.Clone(schema), rest...); err != nil || !bytes.Equal(data, want) {
		t.Fatalf("a small last member: read %d bytes of %d, %v", len(data), len(want), err)
	}
	// A large size that is right, of the last of two members: the content
	// is larger still.
	data, _, err = open(append(gzipOf(t, rest), gzipOf(t, schema)...))
	if want := append(slices.Clone(rest), schema...); err != nil || !bytes.Equal(data, want) {
		t.Fatalf("a large last member: read %d bytes of %d, %v", len(data), len(want), err)
	}
	// A file too short to have a trailer is an error, not a panic.
	if _, _, err := open([]byte{0x1f, 0x8b}); err == nil {
		t.Fatal("opened two bytes")
	}
}

// SHA256 is the checksum of the schema as schemas.lock pins it: of the file
// as upstream serves it, not of what Open makes of it.
func TestSHA256(t *testing.T) {
	s := Embedded()
	f, err := os.Open(testutil.Path("internal/adapter/schemastore/schemas/schemas.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	checked := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		got, err := s.SHA256(fields[1])
		if err != nil || !strings.EqualFold(got, fields[0]) {
			t.Errorf("%s: got %s, %v; schemas.lock has %s", fields[1], got, err, fields[0])
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no schema checked")
	}
	if _, err := s.SHA256("https://example.org/missing.xsd"); err == nil {
		t.Fatal("the checksum of a schema that is not embedded")
	}
	if _, err := s.SHA256("file:///etc/passwd"); err == nil {
		t.Fatal("the checksum of a non-HTTP location")
	}
	broken := New(fstest.MapFS{"example.org/a.xsd.gz": {Data: []byte("not gzip")}})
	if _, err := broken.SHA256("https://example.org/a.xsd"); err == nil {
		t.Fatal("the checksum of a file that is not gzip")
	}
}
