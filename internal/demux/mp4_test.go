package demux

import (
	"bytes"
	"testing"

	"mediapack/internal/fixture"
	"mediapack/internal/model"
)

func mp4Specs() []fixture.MP4TrackSpec {
	return []fixture.MP4TrackSpec{
		{TrackID: 1, Timescale: 24000, Codec: "avc1", Handler: "vide",
			Rotation: 90, CropL: 8, CropT: 4, CropR: 8, CropB: 4,
			ColorPrim: 9, ColorTRC: 16, ColorMx: 9, FullRange: true,
			Count: 6, Size: 50},
		{TrackID: 2, Timescale: 48000, Codec: "mp4a", Handler: "soun",
			ColorPrim: 1, ColorTRC: 1, ColorMx: 1, Count: 5, Size: 24},
	}
}

func TestMP4HappyPath(t *testing.T) {
	raw, want, err := fixture.MP4(mp4Specs())
	if err != nil {
		t.Fatal(err)
	}
	c, err := MP4("sample.mp4", bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if c.Format != "mp4" || len(c.Tracks) != 2 {
		t.Fatalf("container %+v", c)
	}
	video := c.meta(1)
	if video.Codec != "avc1" || video.Handler != "vide" ||
		video.Rotation.Value.Degrees != 90 || video.Rotation.Source != model.SourceMP4 {
		t.Fatalf("video meta wrong: %+v", video)
	}
	if !video.Color.Value.FullRange || video.Color.Value.Primaries != 9 ||
		video.Color.Value.Transfer != 16 {
		t.Fatalf("color wrong: %+v", video.Color)
	}
	// clap clean width 1920-16=1904 -> 8px each side.
	if video.Crop.Value.Left != 8 || video.Crop.Value.Right != 8 ||
		video.Crop.Value.Top != 4 || video.Crop.Value.Bottom != 4 {
		t.Fatalf("crop wrong: %+v", video.Crop.Value)
	}
	if len(c.Samples[1]) != 6 || len(c.Samples[2]) != 5 {
		t.Fatalf("counts %d/%d", len(c.Samples[1]), len(c.Samples[2]))
	}
	s0 := c.Samples[1][0]
	if s0.SrcOffset != want[0].Offset || s0.Size != 50 ||
		!bytes.HasPrefix(s0.Payload, []byte("mp4:1:0:")) {
		t.Fatalf("first sample wrong: %+v", s0)
	}
	// 24000/25 = 960 src ticks per frame -> 3600 master ticks.
	if c.Samples[1][1].DTS != 3600 || s0.Duration != 3600 {
		t.Fatalf("video timing dts1=%d dur=%d", c.Samples[1][1].DTS, s0.Duration)
	}
}

func TestMP4PayloadBytesVerbatim(t *testing.T) {
	raw, _, err := fixture.MP4(mp4Specs())
	if err != nil {
		t.Fatal(err)
	}
	c, err := MP4("sample.mp4", bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range c.Samples[1] {
		got := raw[s.SrcOffset : s.SrcOffset+int64(s.Size)]
		if !bytes.Equal(got, s.Payload) {
			t.Fatalf("sample %d payload altered", s.Index)
		}
	}
}

func TestMP4TruncatedBox(t *testing.T) {
	raw, _, err := fixture.MP4(mp4Specs())
	if err != nil {
		t.Fatal(err)
	}
	// Drop trailing bytes: a box header/body or a referenced sample then runs
	// past EOF. Either truncation or an out-of-range sample table is a valid
	// locatable failure; what matters is the stable code plus a byte offset.
	cut := raw[:len(raw)-37]
	_, err = MP4("cut.mp4", bytes.NewReader(cut), int64(len(cut)))
	me, ok := model.AsMediaError(err)
	if !ok {
		t.Fatalf("want MediaError, got %v", err)
	}
	if (me.Code != model.CodeTruncatedBox &&
		me.Code != model.CodeOutOfRangeSample) || me.Offset < 0 ||
		me.File != "cut.mp4" {
		t.Fatalf("bad error: %+v", me)
	}
}

func TestMP4TruncatedBoxHeader(t *testing.T) {
	raw, _, err := fixture.MP4(mp4Specs())
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt moov's declared size so the structure walk itself overruns.
	// moov starts right after the 24-byte ftyp.
	bad := bytes.Clone(raw)
	bad[24] = 0xff
	bad[25] = 0xff
	bad[26] = 0xff
	bad[27] = 0xff
	_, err = MP4("badbox.mp4", bytes.NewReader(bad), int64(len(bad)))
	me, ok := model.AsMediaError(err)
	if !ok || me.Code != model.CodeTruncatedBox {
		t.Fatalf("want truncation for oversized box, got %v", err)
	}
}

func TestMP4OutOfRangeSampleTable(t *testing.T) {
	raw, _, err := fixture.MP4(mp4Specs())
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the first stco entry to point beyond the file.
	pos := findBox(raw, "stco")
	if pos < 0 {
		t.Fatal("stco not found")
	}
	entry := pos + 8 + 4 + 4 // header + fullbox + count
	bad := bytes.Clone(raw)
	for i := entry; i < entry+4; i++ {
		bad[i] = 0xff
	}
	_, err = MP4("badoff.mp4", bytes.NewReader(bad), int64(len(bad)))
	me, ok := model.AsMediaError(err)
	if !ok || me.Code != model.CodeOutOfRangeSample || me.Track != 1 {
		t.Fatalf("want out-of-range track 1, got %v", err)
	}
}

// findBox locates a leaf box's absolute start by walking the real box tree.
func findBox(raw []byte, want string) int {
	return walkFind(raw, 0, len(raw), want)
}

func walkFind(raw []byte, start, end int, want string) int {
	pos := start
	for pos+8 <= end {
		size := int(uint32(raw[pos])<<24 | uint32(raw[pos+1])<<16 |
			uint32(raw[pos+2])<<8 | uint32(raw[pos+3]))
		typ := string(raw[pos+4 : pos+8])
		if size < 8 || pos+size > end {
			return -1
		}
		if typ == want {
			return pos
		}
		switch typ {
		case "moov", "trak", "mdia", "minf", "stbl":
			if hit := walkFind(raw, pos+8, pos+size, want); hit >= 0 {
				return hit
			}
		}
		pos += size
	}
	return -1
}
