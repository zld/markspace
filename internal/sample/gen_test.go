package sample

import (
	"testing"

	"mediacheck/internal/codec"
	"mediacheck/internal/fault"
)

func TestGenerateAllCodecs(t *testing.T) {
	for _, k := range []codec.Kind{codec.H264, codec.H265, codec.AV1} {
		a, err := Generate(k)
		if err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		if len(a.Units) == 0 || len(a.Index.Offsets) != 3 {
			t.Fatalf("%s: units=%d keyframes=%d", k, len(a.Units), len(a.Index.Offsets))
		}
		if err := a.Index.Validate(a.Units, int64(len(a.Stream))); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		// a unit header must sit exactly at the corruption demo offset
		found := false
		for _, u := range a.Units {
			if u.Offset == HeaderOffset {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: no unit header at offset %d", k, HeaderOffset)
		}
		if len(a.Ticks) != len(a.Units) {
			t.Fatalf("%s: ticks/units mismatch", k)
		}
	}
}

func TestCorruptAtHeaderOffset(t *testing.T) {
	for _, k := range []codec.Kind{codec.H264, codec.H265, codec.AV1} {
		a, err := Generate(k)
		if err != nil {
			t.Fatal(err)
		}
		bad := CorruptAt(a.Stream, HeaderOffset)
		_, err = codec.Units(bad, k)
		fe, ok := err.(*fault.Error)
		if !ok || fe.Code != fault.ErrBadHeader || fe.Offset != HeaderOffset {
			t.Fatalf("%s: got %v", k, err)
		}
	}
}

func TestGenerateDeterministic(t *testing.T) {
	a, _ := Generate(codec.H264)
	b, _ := Generate(codec.H264)
	if string(a.Stream) != string(b.Stream) {
		t.Fatal("streams differ between runs")
	}
	fa := DecodeFrames(codec.AV1, 2)
	fb := DecodeFrames(codec.AV1, 2)
	if string(fa[1]) != string(fb[1]) {
		t.Fatal("frames differ between runs")
	}
}

func TestToneWithinFullScale(t *testing.T) {
	pcm := Tone(4800)
	peak := 0
	for _, s := range pcm {
		v := int(s)
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	if peak == 0 || peak > 32767/2+1 {
		t.Fatalf("peak %d outside expected range", peak)
	}
}
