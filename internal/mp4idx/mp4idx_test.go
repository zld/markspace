package mp4idx_test

import (
	"testing"

	"mediapack/internal/mp4idx"
)

func TestBuildParseRoundtrip(t *testing.T) {
	p := mp4idx.IndexPayload{
		IntervalID:  "seg_0002",
		StartTick:   8589934592, // unwrapped PCR wrap boundary
		EndTick:     8589952592,
		KeyframeOff: []int64{48 * 188},
	}
	data, err := mp4idx.Build(p)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	got, ftyp, err := mp4idx.Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !ftyp {
		t.Fatal("ftyp not parsed")
	}
	if got.IntervalID != p.IntervalID || got.StartTick != p.StartTick {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if len(got.KeyframeOff) != 1 || got.KeyframeOff[0] != 48*188 {
		t.Fatalf("keyframes wrong: %v", got.KeyframeOff)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, _, err := mp4idx.Parse([]byte("not an mp4")); err == nil {
		t.Fatal("garbage must fail MP4 parsing")
	}
}
