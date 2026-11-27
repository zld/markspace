// Package fixture generates deterministic sample media (a minimal MXF KLV
// subset and an ISO-BMFF MP4 subset) without any external files. The formats
// are deliberately small but structurally faithful: real 16-byte SMPTE-style
// keys with BER lengths, real ISO boxes with versioned full boxes.
package fixture

import (
	"encoding/binary"
	"fmt"

	"mediapack/internal/binio"
	"mediapack/internal/klv"
)

// Local tags inside preface/track KLV values.
const (
	tagTimescale uint16 = 0x0101
	tagTrackID   uint16 = 0x0102
	tagCodec     uint16 = 0x0103
	tagHandler   uint16 = 0x0104
	tagRotation  uint16 = 0x0201
	tagCrop      uint16 = 0x0202
	tagColor     uint16 = 0x0203
	tagRTPBase   uint16 = 0x0301
	tagRTPStep   uint16 = 0x0302
	// Sample-group item: index(4) dts(8) duration(8) offset(8) size(4) rtpRaw(4)
	tagSample uint16 = 0x0400
)

// RTPModulus is the fixed 32-bit RTP timestamp modulus (RFC 3550). Clock
// expansion onto an unwrapped axis always uses this value.
const RTPModulus uint64 = 1 << 32

// MXFTrackSpec describes one generated MXF track.
type MXFTrackSpec struct {
	TrackID   uint32
	Timescale uint32
	Codec     string
	Handler   string
	Rotation  int32 // degrees
	CropL     uint32
	CropT     uint32
	CropR     uint32
	CropB     uint32
	ColorPrim uint16
	ColorTRC  uint16
	ColorMx   uint16
	FullRange bool
	RTPBase   uint32
	RTPStep   uint32 // per-sample raw RTP increment; wraps at 2^32
	Count     int
	Size      int
}

// MXFSample is one generated essence sample's provenance.
type MXFSample struct {
	Index    uint32
	DTS      int64
	Duration int64
	Offset   int64
	Size     uint32
	RTPRaw   uint32
}

func writeKLV(w *[]byte, key [16]byte, value []byte) {
	*w = append(*w, key[:]...)
	*w = append(*w, binio.BERLen(uint64(len(value)))...)
	*w = append(*w, value...)
}

func putStr(b *[]byte, s string) {
	*b = append(*b, byte(len(s)))
	*b = append(*b, s...)
}

// buildMXF serializes the minimal MXF image. It returns the bytes plus the
// essence sample table (offsets are relative to file start).
func buildMXF(tracks []MXFTrackSpec) ([]byte, []MXFSample, error) {
	out := make([]byte, 0, 4096)
	// Header partition pack: magic plus version and track count.
	hdr := make([]byte, 0, 16)
	hdr = append(hdr, []byte(klv.HeaderMagic)...)
	hdr = append(hdr, binio.BE32(uint32(len(tracks)))...)
	writeKLV(&out, klv.KeyHeaderPack, hdr)

	var samples []MXFSample
	for _, tr := range tracks {
		pref := make([]byte, 0, 96)
		pref = append(pref, binio.BE32(tr.Timescale)...)
		pref = append(pref, binio.BE32(tr.TrackID)...)
		putStr(&pref, tr.Codec)
		putStr(&pref, tr.Handler)
		pref = append(pref, binio.BE32(uint32(tr.Rotation))...)
		pref = append(pref, binio.BE32(tr.CropL)...)
		pref = append(pref, binio.BE32(tr.CropT)...)
		pref = append(pref, binio.BE32(tr.CropR)...)
		pref = append(pref, binio.BE32(tr.CropB)...)
		pref = append(pref, binio.BE16(tr.ColorPrim)...)
		pref = append(pref, binio.BE16(tr.ColorTRC)...)
		pref = append(pref, binio.BE16(tr.ColorMx)...)
		flag := byte(0)
		if tr.FullRange {
			flag = 1
		}
		pref = append(pref, flag)
		pref = append(pref, binio.BE32(tr.RTPBase)...)
		pref = append(pref, binio.BE32(tr.RTPStep)...)
		writeKLV(&out, klv.KeyPreface, pref)

		// Each essence sample is preceded by its own single-row index KLV,
		// mirroring real MXF where index/essence interleave per item. This
		// keeps the parser stateless: a table KLV names the track for the
		// essence KLV that follows it.
		for i := 0; i < tr.Count; i++ {
			// Natural uint32 arithmetic: a step that crosses 2^32 wraps the
			// raw value, which the timeline later expands deterministically.
			raw := tr.RTPBase + uint32(i)*tr.RTPStep
			row := make([]byte, 40)
			binary.BigEndian.PutUint32(row[0:4], tr.TrackID)
			binary.BigEndian.PutUint32(row[4:8], uint32(i))
			binary.BigEndian.PutUint64(row[8:16],
				uint64(int64(i)*int64(tr.Timescale)/25))
			binary.BigEndian.PutUint64(row[16:24], uint64(tr.Timescale/25))
			// offset(24:32) patched after the KLV framing is written
			binary.BigEndian.PutUint32(row[32:36], uint32(tr.Size))
			binary.BigEndian.PutUint32(row[36:40], raw)
			frameHead := len(klv.KeyTrack) + len(binio.BERLen(uint64(len(row))))

			payload := samplePayload("mxf", tr.TrackID, uint32(i), tr.Size)
			// Table KLV ends here; the essence KLV header (key + BER) follows,
			// then the payload whose file offset we record in the index row.
			tableEnd := int64(len(out)) + int64(frameHead) + int64(len(row))
			essenceHdr := int64(len(klv.KeyEssence)) +
				int64(len(binio.BERLen(uint64(len(payload)))))
			essenceOff := tableEnd + essenceHdr
			binary.BigEndian.PutUint64(row[24:32], uint64(essenceOff))
			writeKLV(&out, klv.KeyTrack, row)
			if got := int64(len(out)); got != tableEnd {
				return nil, nil, fmt.Errorf("table end mismatch: got %d want %d", got, tableEnd)
			}
			writeKLV(&out, klv.KeyEssence, payload)
			if got := int64(len(out)); got != essenceOff+int64(len(payload)) {
				return nil, nil, fmt.Errorf("essence end mismatch: got %d want %d",
					got, essenceOff+int64(len(payload)))
			}
			dts := int64(i) * int64(tr.Timescale) / 25
			samples = append(samples, MXFSample{
				Index: uint32(i), DTS: dts, Duration: int64(tr.Timescale / 25),
				Offset: essenceOff, Size: uint32(tr.Size), RTPRaw: raw,
			})
		}
	}
	return out, samples, nil
}

// samplePayload synthesizes deterministic, incompressible-ish bytes that embed
// their own identity so byte-equality verification can prove no re-encode.
func samplePayload(kind string, track, index uint32, size int) []byte {
	b := make([]byte, size)
	hdr := kind + ":" + u32(track) + ":" + u32(index) + ":"
	copy(b, hdr)
	seed := uint32(0x9e3779b9) ^ track*2654435761 ^ index*40503
	for i := len(hdr); i < size; i++ {
		seed = seed*1103515245 + 12345
		b[i] = byte(seed >> 16)
	}
	return b
}

func u32(v uint32) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	var b [10]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = digits[v%10]
		v /= 10
	}
	return string(b[i:])
}
