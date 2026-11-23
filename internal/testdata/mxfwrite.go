package testdata

import (
	"encoding/binary"
	"os"
	"path/filepath"

	"mediapipe/internal/mxfkv"
)

// MXFSpec describes one deterministic restricted-KLV MXF fixture.
type MXFSpec struct {
	Seed       int64
	Timescale  uint32
	StartNanos int64
	Rotation   int
	Crop       [4]int64
	Colors     []ColorSpec
	// FrameCount is the number of essence items and index records.
	FrameCount int
	FrameSize  int
	KeyEvery   int
	// UnknownKLV injects a vocabulary-outside item that must be tolerated.
	UnknownKLV bool
}

func berLen(n int) []byte {
	if n < 128 {
		return []byte{byte(n)}
	}
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], uint64(n))
	i := 0
	for i < 7 && raw[i] == 0 {
		i++
	}
	out := []byte{byte(0x80 | (8 - i))}
	return append(out, raw[i:]...)
}

func klv(key [16]byte, value []byte) []byte {
	out := make([]byte, 0, 16+8+len(value))
	out = append(out, key[:]...)
	out = append(out, berLen(len(value))...)
	return append(out, value...)
}

func localItem(tag uint16, value []byte) []byte {
	out := make([]byte, 0, 4+len(value))
	var tb [2]byte
	binary.BigEndian.PutUint16(tb[:], tag)
	out = append(out, tb[:]...)
	out = append(out, berLen(len(value))...)
	return append(out, value...)
}

func colorBytes(c ColorSpec) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint16(out[0:], c.Primaries)
	binary.BigEndian.PutUint16(out[2:], c.Transfer)
	binary.BigEndian.PutUint16(out[4:], c.Matrix)
	if c.FullRange {
		out[6] = 1
	}
	return out
}

// WriteMXF writes the restricted KLV fixture to path.
func WriteMXF(path string, spec MXFSpec) error {
	var out []byte
	// Header partition: empty value, declared by key alone.
	out = append(out, klv(mxfkv.KeyHeaderPartition(), nil)...)

	if spec.UnknownKLV {
		out = append(out, klv(mxfkv.KeyUnknown(), []byte{0xde, 0xad, 0xbe, 0xef})...)
	}

	// Preface: version + seed, two u32.
	preface := make([]byte, 8)
	binary.BigEndian.PutUint32(preface[0:], 1)
	binary.BigEndian.PutUint32(preface[4:], uint32(spec.Seed))
	out = append(out, klv(mxfkv.KeyPreface(), preface)...)

	// Track local set.
	startUnits := uint64(float64(spec.StartNanos) * float64(spec.Timescale) / 1e9)
	crop := make([]byte, 32)
	for i, v := range spec.Crop {
		binary.BigEndian.PutUint64(crop[i*8:], uint64(v))
	}
	trackSet := concat([][]byte{
		localItem(mxfkv.TagTrackKind, []byte{mxfkv.KindVideo}),
		localItem(mxfkv.TagTimescale, u32b(spec.Timescale)),
		localItem(mxfkv.TagSampleCount, u32b(uint32(spec.FrameCount))),
		localItem(mxfkv.TagRotation, u32b(uint32(spec.Rotation))),
		localItem(mxfkv.TagCrop, crop),
	})
	for _, c := range spec.Colors {
		trackSet = append(trackSet, localItem(mxfkv.TagColor, colorBytes(c))...)
	}
	out = append(out, klv(mxfkv.KeyTrack(), trackSet)...)

	// Timeline index set: start time plus one record per frame.
	idxHeader := make([]byte, 8)
	binary.BigEndian.PutUint64(idxHeader, startUnits)
	idxHeader = append(idxHeader, u32b(uint32(spec.FrameCount))...)
	recs := make([]byte, 0, spec.FrameCount*mxfkv.IndexRecSize)
	for f := 0; f < spec.FrameCount; f++ {
		rec := make([]byte, mxfkv.IndexRecSize)
		pts := startUnits + uint64(f)*uint64(spec.Timescale)/30
		binary.BigEndian.PutUint64(rec[0:], pts)
		binary.BigEndian.PutUint32(rec[8:], spec.Timescale/30)
		binary.BigEndian.PutUint32(rec[12:], uint32(spec.FrameSize))
		if f%spec.KeyEvery == 0 {
			rec[17] = 1
		}
		recs = append(recs, rec...)
	}
	idxValue := append(idxHeader, recs...)
	idxValue = append(idxValue, localItem(mxfkv.TagIndexCount, u32b(uint32(spec.FrameCount)))...)
	out = append(out, klv(mxfkv.KeyTimelineIndex(), idxValue)...)

	// Essence data: one KLV per frame with deterministic payload.
	for f := 0; f < spec.FrameCount; f++ {
		payload := make([]byte, spec.FrameSize)
		for i := range payload {
			payload[i] = byte(uint32(f*31+i*7+int(spec.Seed)) % 251)
		}
		out = append(out, klv(mxfkv.KeyEssence(), payload)...)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

func u32b(v uint32) []byte {
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, v)
	return out
}
