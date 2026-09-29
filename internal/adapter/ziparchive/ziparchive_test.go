package ziparchive

import (
	"archive/zip"
	"bytes"
	"io"
	"testing"
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
	a, err := Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
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
	if _, err := Open(bytes.NewReader([]byte("not a zip")), 9); err == nil {
		t.Fatal("opened a non-ZIP")
	}
}
