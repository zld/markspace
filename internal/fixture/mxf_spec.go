// Package fixture generates deterministic sample media (a minimal MXF KLV
// subset and an ISO-BMFF MP4 subset) without any external files. The formats
// are deliberately small but structurally faithful: real 16-byte SMPTE-style
// keys with BER lengths, real ISO boxes with versioned full boxes.
package fixture

import (
	"encoding/binary"

	"mediapack/internal/binio"
)

// MXF 16-byte UL keys. The first 12 bytes are a fixed registered-style
// prefix; the last 4 bytes identify the local tag set below.
var (
	mxfPrefix = []byte{0x06, 0x0e, 0x2b, 0x34, 0x01, 0x02, 0x01, 0x01,
		0x0d, 0x01, 0x02, 0x00}
	keyHeaderPack = mxfKey(0x01)
	keyPreface    = mxfKey(0x02)
	keyTrack      = mxfKey(0x03)
	keyEssence    = mxfKey(0x04)
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
	tagRTPMod    uint16 = 0x0302
	// Sample-group item: index(4) dts(8) duration(8) offset(8) size(4) rtpRaw(4)
	tagSample uint16 = 0x0400
)

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
	RTPMod    uint32
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

func mxfKey(tag byte) [16]byte {
	var k [16]byte
	copy(k[:], mxfPrefix)
	k[15] = tag
	return k
}

func klv(w *[]byte, key [16]byte, value []byte) {
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
	hdr = append(hdr, []byte("MXF-HDR-PACK-v1")...)
	hdr = append(hdr, binio.BE32(uint32(len(tracks)))...)
	klv(&out, keyHeaderPack, hdr)

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
		pref = append(pref, binio.BE32(tr.RTPMod)...)
		klv(&out, keyPreface, pref)

		// Track KLV holds the per-sample index table; essence follows.
		// Record each row's offset placeholder so essence offsets can be
		// patched deterministically once the bytes are written.
		tbl := make([]byte, 0, tr.Count*28)
		slots := make([]int, tr.Count)
		for i := 0; i < tr.Count; i++ {
			tbl = append(tbl, binio.BE32(uint32(i))...)
			tbl = append(tbl, binio.BE64(uint64(int64(i)*int64(tr.Timescale)/25))...)
			tbl = append(tbl, binio.BE64(uint64(tr.Timescale/25))...)
			slots[i] = len(out) + len(tbl) // absolute position in out after KLV
			tbl = append(tbl, make([]byte, 8)...) // offset placeholder
			tbl = append(tbl, binio.BE32(uint32(tr.Size))...)
			raw := tr.RTPBase
			if tr.RTPMod != 0 {
				raw = tr.RTPBase + uint32(i)*(tr.RTPMod/25)
			}
			tbl = append(tbl, binio.BE32(raw)...)
		}
		// KLV framing sits between out and tbl; slots must account for it.
		frameHead := len(keyTrack) + len(binio.BERLen(uint64(len(tbl))))
		for i := range slots {
			slots[i] += frameHead
		}
		klv(&out, keyTrack, tbl)

		for i := 0; i < tr.Count; i++ {
			payload := samplePayload("mxf", tr.TrackID, uint32(i), tr.Size)
			off := int64(len(out))
			binary.BigEndian.PutUint64(out[slots[i]:slots[i]+8], uint64(off))
			klv(&out, keyEssence, payload)
			dts := int64(i) * int64(tr.Timescale) / 25
			samples = append(samples, MXFSample{
				Index: uint32(i), DTS: dts, Duration: int64(tr.Timescale / 25),
				Offset: off, Size: uint32(tr.Size),
				RTPRaw: tr.RTPBase + uint32(i)*(tr.RTPMod/25),
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
