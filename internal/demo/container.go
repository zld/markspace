// Package demo generates all input material itself: it synthesizes PCM,
// encodes it with the local FFmpeg, and packetizes the coded output into
// RTP packets or fragmented MP4. No external assets are ever read.
package demo

import (
	"bytes"
	"errors"
	"fmt"
)

// OggPage is one parsed Ogg container page (RFC 3533).
type OggPage struct {
	HeaderType byte
	Granule    uint64
	Serial     uint32
	Sequence   uint32
	Segments   []byte
	Payload    []byte
}

// ParseOgg splits an Ogg bitstream into codec packets, reassembling
// packets that span page boundaries via the 255-byte lacing rule.
func ParseOgg(data []byte) ([][]byte, error) {
	var packets [][]byte
	var carry []byte // packet continued from a previous page
	i := 0
	for i < len(data) {
		if i+27 > len(data) || string(data[i:i+4]) != "OggS" {
			return nil, errors.New("invalid Ogg capture pattern")
		}
		nSeg := int(data[i+26])
		if i+27+nSeg > len(data) {
			return nil, errors.New("truncated Ogg segment table")
		}
		segments := data[i+27 : i+27+nSeg]
		bodyStart := i + 27 + nSeg
		bodyLen := 0
		for _, s := range segments {
			bodyLen += int(s)
		}
		if bodyStart+bodyLen > len(data) {
			return nil, errors.New("truncated Ogg page body")
		}
		body := data[bodyStart : bodyStart+bodyLen]
		i = bodyStart + bodyLen

		// Walk lacing values. Each segment < 255 terminates a packet.
		pos := 0
		for _, s := range segments {
			end := pos + int(s)
			if end > len(body) {
				return nil, errors.New("Ogg lacing exceeds page body")
			}
			carry = append(carry, body[pos:end]...)
			pos = end
			if s < 255 {
				packets = append(packets, carry)
				carry = nil
			}
		}
	}
	// Drop OpusHead and OpusTags header packets.
	var out [][]byte
	for _, p := range packets {
		if bytes.HasPrefix(p, []byte("OpusHead")) || bytes.HasPrefix(p, []byte("OpusTags")) {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// ADTSFrame is one AAC ADTS frame with its raw frame index.
type ADTSFrame struct {
	Raw     []byte // frame payload without the ADTS header
	WithHdr []byte // full frame including header
}

// SplitADTS splits an ADTS AAC-LC bitstream into frames and strips the
// 7- or 9-byte headers, returning raw AAC access units.
func SplitADTS(data []byte) ([][]byte, error) {
	var out [][]byte
	i := 0
	for i < len(data) {
		if i+7 > len(data) || data[i] != 0xFF || data[i+1]&0xF6 != 0xF0 {
			return nil, fmt.Errorf("bad ADTS sync at offset %d", i)
		}
		protectionAbsent := data[i+1] & 0x01
		hdrLen := 7
		if protectionAbsent == 0 {
			hdrLen = 9
		}
		// aac_frame_length is 13 bits spanning bytes 3..5:
		// low 2 bits of b3, all 8 of b4, high 3 bits of b5.
		frameLen := int((uint32(data[i+3]&0x03))<<11 |
			uint32(data[i+4])<<3 |
			uint32(data[i+5]&0xE0)>>5)
		if frameLen == 0 || i+frameLen > len(data) {
			return nil, fmt.Errorf("bad ADTS frame length at offset %d", i)
		}
		out = append(out, data[i+hdrLen:i+frameLen])
		i += frameLen
	}
	return out, nil
}
