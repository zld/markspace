package model

import (
	"encoding/binary"
	"hash/crc32"
)

// Essence packet: the self-describing media packet embedded in MP4 samples
// and MXF essence KLVs. Every packet carries its own integrity check so the
// scanner can localise corruption to a single packet instead of trusting the
// container index alone.
//
// Layout (big-endian):
//
//	0  magic        4B  0xA5C3E10F
//	4  trackID      2B
//	6  flags        2B  (bit0: keyframe)
//	8  pts          8B  in the track's timescale
//	16 payloadLen   4B
//	20 crc32        4B  IEEE, over bytes [0:20) and payload
//	24 payload      payloadLen bytes
const (
	EssenceMagic   uint32 = 0xA5C3E10F
	EssenceHdrSize        = 24
)

// PackEssence serialises one essence packet.
func PackEssence(trackID uint16, keyframe bool, pts int64, payload []byte) []byte {
	buf := make([]byte, EssenceHdrSize+len(payload))
	binary.BigEndian.PutUint32(buf[0:4], EssenceMagic)
	binary.BigEndian.PutUint16(buf[4:6], trackID)
	var flags uint16
	if keyframe {
		flags = 1
	}
	binary.BigEndian.PutUint16(buf[6:8], flags)
	binary.BigEndian.PutUint64(buf[8:16], uint64(pts))
	binary.BigEndian.PutUint32(buf[16:20], uint32(len(payload)))
	crc := crc32.ChecksumIEEE(buf[0:20])
	crc = crc32.Update(crc, crc32.IEEETable, payload)
	binary.BigEndian.PutUint32(buf[20:24], crc)
	copy(buf[24:], payload)
	return buf
}

// EssenceHeader is the parsed fixed part of an essence packet.
type EssenceHeader struct {
	TrackID uint16
	Key     bool
	PTS     int64
	Len     uint32
}

// ParseEssenceHeader validates and parses the fixed header of buf.
// ok=false means the buffer cannot be a valid essence packet.
func ParseEssenceHeader(buf []byte) (h EssenceHeader, ok bool) {
	if len(buf) < EssenceHdrSize {
		return h, false
	}
	if binary.BigEndian.Uint32(buf[0:4]) != EssenceMagic {
		return h, false
	}
	h.TrackID = binary.BigEndian.Uint16(buf[4:6])
	h.Key = binary.BigEndian.Uint16(buf[6:8])&1 != 0
	h.PTS = int64(binary.BigEndian.Uint64(buf[8:16]))
	h.Len = binary.BigEndian.Uint32(buf[16:20])
	return h, true
}

// ValidateEssence checks header, declared length and CRC of one full packet.
func ValidateEssence(buf []byte) bool {
	h, ok := ParseEssenceHeader(buf)
	if !ok {
		return false
	}
	if uint64(len(buf)) != uint64(EssenceHdrSize)+uint64(h.Len) {
		return false
	}
	want := binary.BigEndian.Uint32(buf[20:24])
	got := crc32.ChecksumIEEE(buf[0:20])
	got = crc32.Update(got, crc32.IEEETable, buf[24:])
	return want == got
}
