package fixture

import (
	"bytes"
	"encoding/binary"
)

// Minimal MXF is expressed as KLV triplets using a private 16-byte UL
// space. Only the fields medialab needs are modeled; the layout still
// follows the SMPTE 336M KLV shape (16-byte key, BER length, value) and
// a header/body/footer partition structure so a generic KLV reader works.
var (
	mxfKeyHeader   = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01}
	mxfKeyPreface  = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02}
	mxfKeyBody     = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x03, 0x03, 0x03, 0x03, 0x03, 0x03, 0x03, 0x03, 0x03, 0x03, 0x03, 0x03}
	mxfKeyFooter   = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04}
)

// MXF local tags (last key byte) used by the private metadata pack.
const (
	mxfTagTrackID       = 0x10
	mxfTagEditRateNum   = 0x11
	mxfTagEditRateDen   = 0x12
	mxfTagDuration      = 0x13
	mxfTagOrigin        = 0x14
	mxfTagDisplayWidth  = 0x15
	mxfTagDisplayHeight = 0x16
	mxfTagColorPrim     = 0x17
	mxfTagRotation      = 0x18
	mxfTagIndexTable    = 0x19
	mxfTagEssenceStart  = 0x1A
)

// MXFSpec configures the generated MXF sidecar. Timescale/colour values
// deliberately differ from the MP4 spec so the arbitration policy can be
// exercised: MXF must win and the conflict must be recorded.
type MXFSpec struct {
	Seed        int64
	VideoFrames int
	EditRateNum int64
	EditRateDen int64
	Width       uint32
	Height      uint32
	ColorPrim   uint16
	Rotation    int
	// DropIndex omits the index-table local item to simulate missing index.
	DropIndex bool
}

// DefaultMXFSpec advertises 29.97fps (30000/1001) and different colour
// primaries than the MP4, guaranteeing a metadata conflict.
func DefaultMXFSpec(seed int64) MXFSpec {
	return MXFSpec{
		Seed: seed, VideoFrames: 90,
		EditRateNum: 30000, EditRateDen: 1001,
		Width: 640, Height: 360, ColorPrim: 6, Rotation: 0,
	}
}

// berLen encodes a length using SMPTE long-form BER.
func berLen(n int) []byte {
	var raw []byte
	v := n
	if v == 0 {
		raw = []byte{0}
	}
	for v > 0 {
		raw = append([]byte{byte(v & 0xFF)}, raw...)
		v >>= 8
	}
	out := []byte{byte(0x80 | len(raw))}
	return append(out, raw...)
}

func klv(w *bytes.Buffer, key [16]byte, value []byte) {
	w.Write(key[:])
	w.Write(berLen(len(value)))
	w.Write(value)
}

func localItem(tag byte, value []byte) []byte {
	var b bytes.Buffer
	b.WriteByte(tag)
	b.Write(berLen(len(value)))
	b.Write(value)
	return b.Bytes()
}

func u16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}
func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}
func i32(v int32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(v))
	return b
}
func i64(v int64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(v))
	return b
}

// MXFManifest mirrors the generator ground truth.
type MXFManifest struct {
	EditRateNum, EditRateDen int64
	VideoFrames              int
	ColorPrim                uint16
	Rotation                 int
	EssenceOffset            int64
}

// GenerateMXF builds a header/preface/body/footer KLV stream plus a
// deterministic essence payload. The returned manifest states the offsets
// and values the analyzer must recover.
func GenerateMXF(spec MXFSpec) ([]byte, MXFManifest) {
	rng := NewRand(spec.Seed + 1)

	var preface bytes.Buffer
	preface.Write(localItem(mxfTagTrackID, u32(1)))
	preface.Write(localItem(mxfTagEditRateNum, i64(spec.EditRateNum)))
	preface.Write(localItem(mxfTagEditRateDen, i64(spec.EditRateDen)))
	preface.Write(localItem(mxfTagDuration, i64(int64(spec.VideoFrames))))
	preface.Write(localItem(mxfTagOrigin, i64(0)))
	preface.Write(localItem(mxfTagDisplayWidth, u32(spec.Width)))
	preface.Write(localItem(mxfTagDisplayHeight, u32(spec.Height)))
	preface.Write(localItem(mxfTagColorPrim, u16(spec.ColorPrim)))
	preface.Write(localItem(mxfTagRotation, i32(int32(spec.Rotation))))
	if !spec.DropIndex {
		// One index entry per frame, 8 bytes each (offset + keyframe flag).
		index := make([]byte, spec.VideoFrames*8)
		for f := 0; f < spec.VideoFrames; f++ {
			binary.BigEndian.PutUint32(index[f*8:], uint32(f*mp4VideoSample))
			if f%15 == 0 {
				binary.BigEndian.PutUint32(index[f*8+4:], 1)
			}
		}
		preface.Write(localItem(mxfTagIndexTable, index))
	}

	var file bytes.Buffer
	klv(&file, mxfKeyHeader, []byte{0x00, 0x01})
	klv(&file, mxfKeyPreface, preface.Bytes())

	// Essence body: one deterministic block per frame; its offset inside
	// the whole file is published in the manifest for damage checks.
	body := make([]byte, spec.VideoFrames*mp4VideoSample)
	rng.Fill(body)
	essenceStart := int64(file.Len()) + 16 + int64(len(berLen(len(body))))
	// add essence-start local reference inside a second preface pack
	var ref bytes.Buffer
	ref.Write(localItem(mxfTagEssenceStart, i64(essenceStart)))
	klv(&file, mxfKeyPreface, ref.Bytes())
	klv(&file, mxfKeyBody, body)
	klv(&file, mxfKeyFooter, []byte{0x00, 0x02})

	manifest := MXFManifest{
		EditRateNum: spec.EditRateNum, EditRateDen: spec.EditRateDen,
		VideoFrames: spec.VideoFrames, ColorPrim: spec.ColorPrim,
		Rotation: spec.Rotation, EssenceOffset: essenceStart,
	}
	return file.Bytes(), manifest
}
