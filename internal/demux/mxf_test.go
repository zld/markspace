package demux

import (
	"bytes"
	"testing"

	"mediapack/internal/fixture"
	"mediapack/internal/klv"
	"mediapack/internal/model"
)

func mxfSpecs() []fixture.MXFTrackSpec {
	return []fixture.MXFTrackSpec{
		{
			TrackID: 1, Timescale: 25, Codec: "vraw", Handler: "pict",
			Rotation: 90, CropL: 4, CropT: 2, CropR: 4, CropB: 2,
			ColorPrim: 9, ColorTRC: 16, ColorMx: 9, FullRange: true,
			RTPBase: 1000, RTPStep: 3600, Count: 12, Size: 64,
		},
		{
			TrackID: 2, Timescale: 48000, Codec: "pcm ", Handler: "soun",
			ColorPrim: 1, ColorTRC: 1, ColorMx: 1,
			RTPBase: 50, RTPStep: 1920, Count: 8, Size: 32,
		},
	}
}

// findKLV returns the start offset of the first run using key, walking real
// KLV run boundaries so payload bytes cannot produce a false match.
func findKLV(raw []byte, key [16]byte) int {
	pos := 0
	for pos+17 <= len(raw) {
		runStart := pos
		if bytes.Equal(raw[pos:pos+16], key[:]) {
			return runStart
		}
		first := raw[pos+16]
		hdr := 1
		length := uint64(first)
		if first >= 0x80 {
			n := int(first & 0x7f)
			length = 0
			for i := 0; i < n; i++ {
				length = length<<8 | uint64(raw[pos+17+i])
			}
			hdr = 1 + n
		}
		pos = runStart + 16 + hdr + int(length)
	}
	return -1
}

func TestMXFHappyPath(t *testing.T) {
	raw, want, err := fixture.MXF(mxfSpecs())
	if err != nil {
		t.Fatal(err)
	}
	c, err := MXF("sample.mxf", bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if c.Format != "mxf" || len(c.Tracks) != 2 {
		t.Fatalf("container %+v", c)
	}
	video := c.meta(1)
	if video.Timescale != 25 || video.Rotation.Value.Degrees != 90 ||
		video.Rotation.Source != model.SourceMXF || video.Crop.Value.Left != 4 ||
		!video.Color.Value.FullRange || video.Color.Value.Primaries != 9 {
		t.Fatalf("video metadata wrong: %+v", video)
	}
	if got := len(c.Samples[1]); got != 12 || len(c.Samples[2]) != 8 {
		t.Fatalf("sample counts %d/%d", len(c.Samples[1]), len(c.Samples[2]))
	}
	s0 := c.Samples[1][0]
	if s0.SrcOffset != want[0].Offset || s0.Size != 64 ||
		!bytes.HasPrefix(s0.Payload, []byte("mxf:1:0:")) {
		t.Fatalf("first sample wrong: %+v", s0)
	}
	// 25 Hz -> 90 kHz: each frame is exactly 3600 master ticks.
	if s0.DTS != 0 || c.Samples[1][1].DTS != 3600 || s0.Duration != 3600 {
		t.Fatalf("time scaling wrong: dts0=%d dts1=%d dur=%d",
			s0.DTS, c.Samples[1][1].DTS, s0.Duration)
	}
	// Audio rows use the same 25 fps cadence: 1920 src ticks at 48 kHz,
	// which converts to 3600 master ticks per row.
	if c.Samples[2][0].Duration != 3600 {
		t.Fatalf("audio duration %d", c.Samples[2][0].Duration)
	}
}

func TestMXFTruncationReportsOffset(t *testing.T) {
	raw, _, err := fixture.MXF(mxfSpecs())
	if err != nil {
		t.Fatal(err)
	}
	cut := raw[:len(raw)/2]
	_, err = MXF("cut.mxf", bytes.NewReader(cut), int64(len(cut)))
	me, ok := model.AsMediaError(err)
	if !ok {
		t.Fatalf("want MediaError, got %v", err)
	}
	if me.Code != model.CodeTruncatedBox || me.Offset < 0 || me.File != "cut.mxf" {
		t.Fatalf("bad error: %+v", me)
	}
}

func TestMXFDeclaredLengthBeyondEOF(t *testing.T) {
	raw, _, err := fixture.MXF(mxfSpecs())
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the LAST essence run so no earlier offset check can fire first:
	// walk all runs, find the final essence KLV, inflate its BER length.
	last := -1
	pos := 0
	for pos+17 <= len(raw) {
		runStart := pos
		first := raw[pos+16]
		hdr := 1
		length := uint64(first)
		if first >= 0x80 {
			n := int(first & 0x7f)
			length = 0
			for i := 0; i < n; i++ {
				length = length<<8 | uint64(raw[pos+17+i])
			}
			hdr = 1 + n
		}
		if bytes.Equal(raw[pos:pos+16], klv.KeyEssence[:]) {
			last = runStart
		}
		pos = runStart + 16 + hdr + int(length)
	}
	if last < 0 {
		t.Fatal("no essence run found")
	}
	bad := bytes.Clone(raw)
	// 0x82 = long form, two length bytes; claim far more than the file holds.
	bad[last+16] = 0x82
	bad[last+17] = 0xff
	bad[last+18] = 0xff
	_, err = MXF("badlen.mxf", bytes.NewReader(bad), int64(len(bad)))
	me, ok := model.AsMediaError(err)
	if !ok || me.Code != model.CodeTruncatedBox {
		t.Fatalf("want truncation, got %v", err)
	}
}

func TestMXFOutOfRangeSampleOffset(t *testing.T) {
	raw, _, err := fixture.MXF(mxfSpecs())
	if err != nil {
		t.Fatal(err)
	}
	idx := findKLV(raw, klv.KeyTrack)
	if idx < 0 {
		t.Fatal("no index run found")
	}
	val := idx + 16 + 1 // key + short-form BER length byte
	bad := bytes.Clone(raw)
	for i := val + 24; i < val+32; i++ { // offset field of the 40-byte row
		bad[i] = 0xff
	}
	_, err = MXF("badoff.mxf", bytes.NewReader(bad), int64(len(bad)))
	me, ok := model.AsMediaError(err)
	if !ok || me.Code != model.CodeOutOfRangeSample || me.Track != 1 {
		t.Fatalf("want out-of-range on track 1, got %v", err)
	}
}

func TestMXFEssenceWithoutIndex(t *testing.T) {
	raw, _, err := fixture.MXF(mxfSpecs())
	if err != nil {
		t.Fatal(err)
	}
	idx := findKLV(raw, klv.KeyTrack)
	if idx < 0 {
		t.Fatal("no index run found")
	}
	rowLen := int(raw[idx+16])
	end := idx + 16 + 1 + rowLen
	bad := append(raw[:idx:idx], raw[end:]...)
	_, err = MXF("orphan.mxf", bytes.NewReader(bad), int64(len(bad)))
	me, ok := model.AsMediaError(err)
	if !ok || me.Code != model.CodeMalformedContainer {
		t.Fatalf("want malformed, got %v", err)
	}
}
