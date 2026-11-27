package fixture

import (
	"encoding/binary"
)

func boxBytes(typ string, payload []byte) []byte {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr, uint32(8+len(payload)))
	copy(hdr[4:], typ)
	return append(hdr, payload...)
}

// patchStco walks the assembled file's box tree and fills each trak's stco
// entries using the offsets computed from the same layout.
func patchStco(buf []byte, layouts []laid, mdatPayloadStart int64) error {
	_ = mdatPayloadStart
	var stcos []int
	walkBoxes(buf, 0, len(buf), func(typ string, start int) {
		if typ == "stco" {
			stcos = append(stcos, start)
		}
	})
	if len(stcos) != len(layouts) {
		return errStcoTracks(len(stcos), len(layouts))
	}
	for ti, abs := range stcos {
		// stco layout: header(8) fullbox(4) count(4) then u32 entries.
		entry := abs + 8 + 4 + 4
		n := binary.BigEndian.Uint32(buf[abs+12 : abs+16])
		off := layouts[ti].offsets
		if int(n) != len(off) {
			return errStcoCount(ti, int(n), len(off))
		}
		for i := uint32(0); i < n; i++ {
			pos := entry + int(i)*4
			binary.BigEndian.PutUint32(buf[pos:pos+4], uint32(off[i]))
		}
	}
	return nil
}

// walkBoxes visits every box in [start,end) recursively, reporting type and
// absolute start. Container boxes are descended into; leaf boxes are skipped.
func walkBoxes(buf []byte, start, end int, visit func(typ string, start int)) {
	pos := start
	for pos+8 <= end {
		size := int(binary.BigEndian.Uint32(buf[pos : pos+4]))
		typ := string(buf[pos+4 : pos+8])
		if size < 8 || pos+size > end {
			return
		}
		visit(typ, pos)
		if isContainerBox(typ) {
			walkBoxes(buf, pos+8, pos+size, visit)
		}
		pos += size
	}
}

func isContainerBox(typ string) bool {
	switch typ {
	case "moov", "trak", "mdia", "minf", "stbl":
		return true
	}
	return false
}

type stcoCountError struct{ ti, got, want int }

func (e *stcoCountError) Error() string { return "stco entry count mismatch" }

func errStcoCount(ti, got, want int) error { return &stcoCountError{ti, got, want} }

type stcoTracksError struct{ got, want int }

func (e *stcoTracksError) Error() string { return "stco track count mismatch" }

func errStcoTracks(got, want int) error { return &stcoTracksError{got, want} }
