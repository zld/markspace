package ebml

import (
	"encoding/binary"

	"medtrace/internal/mederr"
)

// Block is the decoded common payload of Block and SimpleBlock elements.
type Block struct {
	TrackNum uint64
	Timecode int16 // cluster-relative signed timecode
	Flags    uint8
	Payload  []byte
}

// ParseBlock decodes a Matroska block body: track VINT, int16 timecode, flags.
func ParseBlock(body []byte) (*Block, error) {
	track, w, unknown, err := DecodeVINT(body)
	if err != nil {
		return nil, err
	}
	if unknown {
		return nil, mederr.New(mederr.CatInvalidVINT, "block track number uses unknown-length VINT")
	}
	if len(body) < w+3 {
		return nil, mederr.New(mederr.CatTruncatedHeader, "block needs %d header bytes, got %d", w+3, len(body))
	}
	tc := int16(binary.BigEndian.Uint16(body[w:]))
	return &Block{
		TrackNum: track,
		Timecode: tc,
		Flags:    body[w+2],
		Payload:  body[w+3:],
	}, nil
}
