// Package index builds a random-access, content-addressed sample index from
// scan output. Index files are fixed-width binary so lookups are O(1) seeks,
// and loading requires reading only a small header plus one cache trailer.
package index

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"mediacore/internal/clock"
)

// SchemaVersion is bumped whenever the on-disk record layout changes. It is
// stored in every index header and checked before a cache is trusted.
const SchemaVersion uint32 = clock.BasisVersion

const (
	magic       = "MCIDX001"
	headerSize  = 64
	recordSize  = 44
	trailerSize = 32 // SHA-256 of everything preceding the trailer
)

// Header is the fixed 64-byte prefix of every index file.
type Header struct {
	Magic         [8]byte
	SchemaVersion uint32
	TrackID       uint32
	Timescale     uint32
	SampleCount   uint32
	_             [12]byte // reserved, must be zero
	InputDigest   [32]byte
}

// Record is the fixed-width per-sample entry (44 bytes). All multi-byte
// values are big-endian; signed times are stored as two's complement.
//
//	0..8   Offset   absolute MP4 payload offset
//	8..12  Size     physical mdat bytes
//	12..16 WireSize logical network payload bytes
//	16..24 DTS      decode time, track ticks
//	24..32 DTSNanos decode time, unified nanoseconds
//	32..40 CTSNanos composition time, unified nanoseconds
//	40..44 Flags    bit0 = sync/key frame
type Record struct {
	Offset   uint64
	Size     uint32
	WireSize uint32
	DTS      int64
	DTSNanos int64
	CTSNanos int64
	Flags    uint32
}

const flagSync uint32 = 1

// Encode writes the 44-byte big-endian representation.
func (r Record) Encode(out []byte) {
	_ = out[recordSize-1]
	binary.BigEndian.PutUint64(out[0:8], r.Offset)
	binary.BigEndian.PutUint32(out[8:12], r.Size)
	binary.BigEndian.PutUint32(out[12:16], r.WireSize)
	binary.BigEndian.PutUint64(out[16:24], uint64(r.DTS))
	binary.BigEndian.PutUint64(out[24:32], uint64(r.DTSNanos))
	binary.BigEndian.PutUint64(out[32:40], uint64(r.CTSNanos))
	binary.BigEndian.PutUint32(out[40:44], r.Flags&flagSync)
}

// Decode parses a 44-byte record.
func Decode(in []byte) Record {
	_ = in[recordSize-1]
	return Record{
		Offset:   binary.BigEndian.Uint64(in[0:8]),
		Size:     binary.BigEndian.Uint32(in[8:12]),
		WireSize: binary.BigEndian.Uint32(in[12:16]),
		DTS:      int64(binary.BigEndian.Uint64(in[16:24])),
		DTSNanos: int64(binary.BigEndian.Uint64(in[24:32])),
		CTSNanos: int64(binary.BigEndian.Uint64(in[32:40])),
		Flags:    binary.BigEndian.Uint32(in[40:44]),
	}
}

// IsSync reports whether the record is an independently decodable key frame.
func (r Record) IsSync() bool { return r.Flags&flagSync != 0 }

// Digest returns a hex digest over the canonical header + records so that
// repeated runs over identical input can be compared byte-for-byte.
func Digest(h Header, records []Record) string {
	sum := sha256.New()
	var buf [recordSize]byte
	hb := make([]byte, headerSize)
	encodeHeader(h, hb)
	sum.Write(hb)
	for _, r := range records {
		r.Encode(buf[:])
		sum.Write(buf[:])
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func encodeHeader(h Header, out []byte) {
	copy(out[0:8], magic)
	binary.BigEndian.PutUint32(out[8:12], h.SchemaVersion)
	binary.BigEndian.PutUint32(out[12:16], h.TrackID)
	binary.BigEndian.PutUint32(out[16:20], h.Timescale)
	binary.BigEndian.PutUint32(out[20:24], h.SampleCount)
	copy(out[24:56], h.InputDigest[:])
}

func decodeHeader(in []byte) (Header, error) {
	var h Header
	if string(in[0:8]) != magic {
		return h, clock.ErrRecoverablef("index.header", "bad magic %q", string(in[0:8]))
	}
	h.SchemaVersion = binary.BigEndian.Uint32(in[8:12])
	h.TrackID = binary.BigEndian.Uint32(in[12:16])
	h.Timescale = binary.BigEndian.Uint32(in[16:20])
	h.SampleCount = binary.BigEndian.Uint32(in[20:24])
	copy(h.InputDigest[:], in[24:56])
	return h, nil
}

// ExpectedSize is the exact byte length of a complete index for n records.
func ExpectedSize(n uint32) int64 {
	return int64(headerSize) + int64(n)*recordSize + trailerSize
}

func (h Header) String() string {
	return fmt.Sprintf("schema=%d track=%d samples=%d input=%x", h.SchemaVersion, h.TrackID, h.SampleCount, h.InputDigest[:8])
}
