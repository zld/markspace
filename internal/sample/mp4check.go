package sample

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/abema/go-mp4"
)

// AuxiliaryMP4FileTypeBox returns a tiny generated ISO-BMFF ftyp box. It is
// only used by the container helper test that exercises go-mp4; the validation
// kernel itself processes Matroska/WebM EBML and RTP metadata.
func AuxiliaryMP4FileTypeBox() []byte {
	major := []byte("isom")
	minor := []byte{0x00, 0x00, 0x02, 0x00}
	compatible := []byte("isom")
	payload := append(append(append([]byte{}, major...), minor...), compatible...)
	box := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(box[0:4], uint32(len(box)))
	copy(box[4:8], "ftyp")
	copy(box[8:], payload)
	return box
}

// InspectFirstMP4Box uses go-mp4 as an auxiliary container parser in tests and
// diagnostics. It does not participate in Matroska validation.
func InspectFirstMP4Box(data []byte) (*mp4.BoxInfo, error) {
	info, err := mp4.ReadBoxInfo(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("read MP4 box info: %w", err)
	}
	return info, nil
}
