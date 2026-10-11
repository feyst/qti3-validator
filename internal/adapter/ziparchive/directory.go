package ziparchive

import (
	"bufio"
	"encoding/binary"
	"io"
)

// Signatures and fixed sizes of the ZIP records read here, from the ZIP
// specification (APPNOTE.TXT, section 4.3).
const (
	endSig       = 0x06054b50 // end of central directory record
	end64LocSig  = 0x07064b50 // ZIP64 end of central directory locator
	end64Sig     = 0x06064b50 // ZIP64 end of central directory record
	headerSig    = 0x02014b50 // central directory file header
	endLen       = 22         // without the comment
	end64LocLen  = 20
	end64Len     = 56
	headerLen    = 46 // without name, extra field and comment
	maxEndSearch = 65 * 1024
)

// tooManyEntries reports whether the central directory of the ZIP in r has
// more than maxEntries records, without keeping them in memory.
//
// archive/zip trusts neither the count nor the size the end of central
// directory record declares: it reads records from the start of the
// directory until one is not a file header, and compares only the low 16
// bits of the count. So after the declared count, the records are counted
// the way archive/zip will read them, from where it will start, stopping
// past maxEntries. A damaged archive is left to archive/zip to refuse.
func tooManyEntries(r io.ReaderAt, size int64, maxEntries int) bool {
	end, ok := readEnd(r, size)
	if !ok {
		return false
	}
	if maxEntries < 0 || end.records > uint64(maxEntries) {
		return true
	}
	// archive/zip starts where the directory ends minus its size; if that
	// leaves room for a prefix, as in a self-extracting archive, it may start
	// at the declared offset instead.
	dirSize, dirOffset := int64(end.dirSize), int64(end.dirOffset) //nolint:gosec // readEnd checked that they fit
	start := end.offset - dirSize
	if start < 0 || start >= size {
		return false
	}
	starts := []int64{start}
	if start > dirOffset {
		starts = append(starts, dirOffset)
	}
	for _, s := range starts {
		if countHeaders(r, size, s, maxEntries) > maxEntries {
			return true
		}
	}
	return false
}

// directoryEnd is what the end of central directory records declare.
type directoryEnd struct {
	offset    int64  // of the end record, or of the ZIP64 end record
	records   uint64 // entries in the central directory
	dirSize   uint64 // bytes of the central directory
	dirOffset uint64 // of the central directory, from the archive's start
}

// readEnd finds and reads the end of central directory as archive/zip does:
// the last end record in the final 1 KiB, else in the final 65 KiB, and the
// ZIP64 end record its locator points at. ok is false where archive/zip
// fails too.
func readEnd(r io.ReaderAt, size int64) (end directoryEnd, ok bool) {
	var buf []byte
	end.offset = -1
	for _, n := range []int64{1024, maxEndSearch} {
		n = min(n, size)
		buf = make([]byte, n)
		if _, err := r.ReadAt(buf, size-n); err != nil && err != io.EOF {
			return end, false
		}
		if p := findEnd(buf); p >= 0 {
			buf, end.offset = buf[p:], size-n+int64(p)
			break
		}
		if n == size {
			break
		}
	}
	if end.offset < 0 {
		return end, false
	}
	le := binary.LittleEndian
	end.records = uint64(le.Uint16(buf[10:]))
	end.dirSize = uint64(le.Uint32(buf[12:]))
	end.dirOffset = uint64(le.Uint32(buf[16:]))
	if end.records == 0xffff || end.dirSize == 0xffffffff || end.dirOffset == 0xffffffff {
		if !readEnd64(r, &end) {
			return end, false
		}
	}
	const maxInt64 = 1<<63 - 1
	return end, end.dirSize <= maxInt64 && end.dirOffset <= maxInt64
}

// findEnd returns the position of the last end of central directory record
// in b whose comment fits, or -1; like archive/zip, it gives up at the first
// one whose comment does not fit.
func findEnd(b []byte) int {
	for i := len(b) - endLen; i >= 0; i-- {
		if binary.LittleEndian.Uint32(b[i:]) == endSig {
			if comment := int(binary.LittleEndian.Uint16(b[i+endLen-2:])); i+endLen+comment > len(b) {
				return -1
			}
			return i
		}
	}
	return -1
}

// readEnd64 replaces the counts of end with those of the ZIP64 end record,
// if a locator precedes the end record. It returns false for a locator that
// points at no ZIP64 end record.
func readEnd64(r io.ReaderAt, end *directoryEnd) bool {
	le := binary.LittleEndian
	loc := make([]byte, end64LocLen)
	if end.offset < end64LocLen {
		return true
	}
	if _, err := r.ReadAt(loc, end.offset-end64LocLen); err != nil {
		return false
	}
	if le.Uint32(loc) != end64LocSig || le.Uint32(loc[4:]) != 0 || le.Uint32(loc[16:]) != 1 {
		return true // not a ZIP64 archive
	}
	offset := int64(le.Uint64(loc[8:])) //nolint:gosec // a negative offset fails to read below
	rec := make([]byte, end64Len)
	if _, err := r.ReadAt(rec, offset); err != nil || le.Uint32(rec) != end64Sig {
		return false
	}
	end.offset = offset
	end.records = le.Uint64(rec[32:])
	end.dirSize = le.Uint64(rec[40:])
	end.dirOffset = le.Uint64(rec[48:])
	return true
}

// countHeaders counts the consecutive central directory file headers from
// start, up to maxEntries+1.
func countHeaders(r io.ReaderAt, size, start int64, maxEntries int) int {
	le := binary.LittleEndian
	br := bufio.NewReader(io.NewSectionReader(r, start, size-start))
	var h [headerLen]byte
	n := 0
	for n <= maxEntries {
		if _, err := io.ReadFull(br, h[:]); err != nil || le.Uint32(h[:]) != headerSig {
			break
		}
		// The name, extra field and comment follow.
		if _, err := br.Discard(int(le.Uint16(h[28:])) + int(le.Uint16(h[30:])) + int(le.Uint16(h[32:]))); err != nil {
			break
		}
		n++
	}
	return n
}
