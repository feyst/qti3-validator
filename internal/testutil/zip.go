package testutil

import (
	"bytes"
	"encoding/binary"
)

// CrowdedZIP returns a ZIP whose central directory has records entries,
// all pointing at one empty local file "a", the shape of an archive that
// costs little to send and much to open. The end of central directory
// record declares declared entries; with zip64 it is a ZIP64 archive, whose
// counts are not truncated to 16 bits.
func CrowdedZIP(records, declared int, zip64 bool) []byte {
	le := binary.LittleEndian
	var b bytes.Buffer
	put := func(vs ...any) {
		for _, v := range vs {
			_ = binary.Write(&b, le, v)
		}
	}
	// Local file header: version, flags, method, time, date, CRC, sizes,
	// name length, extra length, name.
	put(uint32(0x04034b50), uint16(20), uint16(0), uint16(0), uint16(0), uint16(0),
		uint32(0), uint32(0), uint32(0), uint16(1), uint16(0))
	b.WriteString("a")

	// One central directory record, repeated: versions, flags, method, time,
	// date, CRC, sizes, name/extra/comment lengths, disk, attributes, offset.
	var rec bytes.Buffer
	for _, v := range []any{
		uint32(0x02014b50), uint16(20), uint16(20), uint16(0), uint16(0), uint16(0), uint16(0),
		uint32(0), uint32(0), uint32(0), uint16(1), uint16(0), uint16(0), uint16(0), uint16(0),
		uint32(0), uint32(0),
	} {
		_ = binary.Write(&rec, le, v)
	}
	rec.WriteString("a")
	cdStart := b.Len()
	b.Write(bytes.Repeat(rec.Bytes(), records))
	cdSize := b.Len() - cdStart

	if zip64 {
		u64 := func(v int) uint64 { return uint64(v) } //nolint:gosec // the values are not negative
		end64 := b.Len()
		put(uint32(0x06064b50), uint64(44), uint16(45), uint16(45), uint32(0), uint32(0),
			u64(declared), u64(declared), u64(cdSize), u64(cdStart))
		put(uint32(0x07064b50), uint32(0), u64(end64), uint32(1))
		put(uint32(0x06054b50), uint16(0), uint16(0), uint16(0xffff), uint16(0xffff),
			uint32(0xffffffff), uint32(0xffffffff), uint16(0))
		return b.Bytes()
	}
	d16 := uint16(declared) //nolint:gosec // truncating to 16 bits is what the format does
	put(uint32(0x06054b50), uint16(0), uint16(0), d16, d16, uint32(cdSize), uint32(cdStart), uint16(0))
	return b.Bytes()
}
