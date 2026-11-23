package ebml

import (
	"bytes"
	"testing"
)

func TestHugeChildSizeRejected(t *testing.T) {
	// Info with a child whose declared size is enormous (8-byte VINT, near max),
	// which would overflow a naive offset+size check.
	info := []byte{0x2A, 0xD7, 0xB1} // TimecodeScale id
	info = append(info, EncodeSizeWidth(0x00FFFFFFFFFFFFFF, 8, false)...)
	info = append(info, 0x01)
	full := append([]byte{}, el(IDEBML, append(el(IDDocType, []byte("matroska")), el(IDDocTypeVersion, uints(4))...))...)
	segBody := el(IDInfo, info)
	tracks := el(IDTracks, el(IDTrackEntry, append(el(IDTrackNumber, uints(1)), el(IDTrackType, uints(1))...)))
	segBody = append(segBody, tracks...)
	full = append(full, el(IDSegment, segBody)...)
	if _, err := Parse(bytes.NewReader(full), DefaultParseOptions(), nil); err == nil {
		t.Fatal("huge child size accepted")
	}
}
