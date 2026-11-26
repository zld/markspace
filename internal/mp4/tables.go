package mp4

import (
	"encoding/binary"
)

type trackBuilder struct {
	id        uint32
	kind      string
	codec     string
	timescale uint32
	width     uint32
	height    uint32
}

func (s *scanner) parseTKHD(box BoxRef, tb *trackBuilder) {
	buf := make([]byte, 92)
	if err := s.readAt(box.Offset+box.HeaderSize, buf); err != nil {
		return
	}
	if buf[0] == 1 {
		tb.id = binary.BigEndian.Uint32(buf[20:24])
	} else {
		tb.id = binary.BigEndian.Uint32(buf[12:16])
	}
}

func (s *scanner) parseHDLR(box BoxRef, tb *trackBuilder) {
	var buf [12]byte
	if err := s.readAt(box.Offset+box.HeaderSize, buf[:]); err != nil {
		return
	}
	switch fourCC(buf[8:12]) {
	case "vide":
		tb.kind = "video"
	case "soun":
		tb.kind = "audio"
	default:
		tb.kind = fourCC(buf[8:12])
	}
}

func (s *scanner) parseSTSD(box BoxRef, tb *trackBuilder) {
	var buf [40]byte
	start := box.Offset + box.HeaderSize
	if err := s.readAt(start, buf[:16]); err != nil {
		return
	}
	if binary.BigEndian.Uint32(buf[4:8]) == 0 {
		return
	}
	if err := s.readAt(start+8, buf[:40]); err != nil {
		return
	}
	tb.codec = fourCC(buf[4:8]) // entry header begins at payload+8
	if tb.kind == "video" {
		tb.width = uint32(binary.BigEndian.Uint16(buf[32:34]))
		tb.height = uint32(binary.BigEndian.Uint16(buf[34:36]))
	}
}

func (tb *trackBuilder) build(s *scanner) (*Track, error) {
	kind := tb.kind
	if kind == "" {
		kind = "unknown"
	}
	return &Track{
		ID: tb.id, Kind: kind, Codec: tb.codec, Timescale: tb.timescale,
		Width: tb.width, Height: tb.height,
	}, nil
}
