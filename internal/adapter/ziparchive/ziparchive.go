// Package ziparchive reads content packages with archive/zip, and
// implements app.Archive.
package ziparchive

import (
	"archive/zip"
	"io"

	"qti3-validator/internal/app"
)

var _ app.ArchiveOpener = Open

// Open reads the central directory of a ZIP file. It first counts the
// directory's records, and refuses an archive with more than maxEntries
// before archive/zip keeps a File for each of them.
func Open(r io.ReaderAt, size int64, maxEntries int) (app.Archive, error) {
	if tooManyEntries(r, size, maxEntries) {
		return nil, app.ErrTooManyEntries
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	return archive{zr}, nil
}

type archive struct{ zr *zip.Reader }

func (a archive) Entries() []app.ArchiveEntry {
	out := make([]app.ArchiveEntry, len(a.zr.File))
	for i, f := range a.zr.File {
		out[i] = entry{f}
	}
	return out
}

type entry struct{ f *zip.File }

func (e entry) Name() string                 { return e.f.Name }
func (e entry) IsDir() bool                  { return e.f.FileInfo().IsDir() }
func (e entry) DeclaredSize() uint64         { return e.f.UncompressedSize64 }
func (e entry) Open() (io.ReadCloser, error) { return e.f.Open() }
