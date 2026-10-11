package app

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"testing/iotest"
)

// A document is read whole whatever size it is said to have: the size only
// chooses the buffer it is read into, which holds the document at once when
// the size is right.
func TestReadAllWithSize(t *testing.T) {
	doc := bytes.Repeat([]byte("<p>text</p>"), 1000)
	for _, size := range []int64{0, -1, 1, int64(len(doc)) - 1, int64(len(doc)), int64(len(doc)) + 1, 10 * int64(len(doc))} {
		for name, r := range map[string]io.Reader{
			"at once":       bytes.NewReader(doc),
			"byte by byte":  iotest.OneByteReader(bytes.NewReader(doc)),
			"end with data": iotest.DataErrReader(bytes.NewReader(doc)),
		} {
			got, err := readAll(r, size)
			if err != nil || !bytes.Equal(got, doc) {
				t.Fatalf("size %d, %s: read %d bytes of %d: %v", size, name, len(got), len(doc), err)
			}
			if size == int64(len(doc)) && cap(got) != len(doc)+1 {
				t.Errorf("size %d, %s: a buffer of %d bytes for a document of %d", size, name, cap(got), len(doc))
			}
		}
	}
	broken := errors.New("broken")
	got, err := readAll(io.MultiReader(bytes.NewReader(doc[:100]), iotest.ErrReader(broken)), int64(len(doc)))
	if !errors.Is(err, broken) || len(got) != 100 {
		t.Fatalf("read %d bytes: %v; want the error of the reader", len(got), err)
	}
}
