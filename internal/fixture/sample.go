// Package fixture builds deterministic, self-describing MP4/MOV input files
// for offline regression. No external media is referenced: every sample is a
// small binary frame whose integrity the recovery layer can verify.
package fixture

import (
	"encoding/binary"
	"hash/crc32"
)

// SampleMagic prefixes every generated sample. A scanner that has lost the
// container index can resync on this 8-byte sequence.
const SampleMagic = "TRCSMPL!"

// SampleHeaderSize is magic(8) + index(4) + track(4) + length(4) + crc(4).
const SampleHeaderSize = 24

// SampleFrame is the in-file layout of one generated sample. It is deliberately
// fixed-width-header so recovery never depends on a surviving sample table.
type SampleFrame struct {
	Index   uint32
	TrackID uint32
	Payload []byte
}

// Encode renders a sample to bytes. Length covers payload only; the CRC is
// computed over every preceding byte including the magic.
func (f SampleFrame) Encode() []byte {
	out := make([]byte, SampleHeaderSize+len(f.Payload))
	copy(out[0:8], SampleMagic)
	binary.BigEndian.PutUint32(out[8:12], f.Index)
	binary.BigEndian.PutUint32(out[12:16], f.TrackID)
	binary.BigEndian.PutUint32(out[16:20], uint32(len(f.Payload)))
	copy(out[20:20+len(f.Payload)], f.Payload)
	csum := crc32.ChecksumIEEE(out[:20+len(f.Payload)])
	binary.BigEndian.PutUint32(out[20+len(f.Payload):], csum)
	return out
}

// VerifyFrame checks a raw sample blob: magic, self-described length and CRC.
// It returns the decoded frame on success.
func VerifyFrame(raw []byte) (SampleFrame, bool) {
	if len(raw) < SampleHeaderSize {
		return SampleFrame{}, false
	}
	if string(raw[0:8]) != SampleMagic {
		return SampleFrame{}, false
	}
	payloadLen := binary.BigEndian.Uint32(raw[16:20])
	if int(payloadLen)+SampleHeaderSize != len(raw) {
		return SampleFrame{}, false
	}
	want := binary.BigEndian.Uint32(raw[20+payloadLen:])
	got := crc32.ChecksumIEEE(raw[:20+payloadLen])
	if want != got {
		return SampleFrame{}, false
	}
	return SampleFrame{
		Index:   binary.BigEndian.Uint32(raw[8:12]),
		TrackID: binary.BigEndian.Uint32(raw[12:16]),
		Payload: append([]byte(nil), raw[20:20+payloadLen]...),
	}, true
}
