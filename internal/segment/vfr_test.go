package segment

import (
	"testing"
)

// cum builds cumulative sample counts from per-step deltas.
func cum(start uint64, deltas ...uint64) []uint64 {
	out := []uint64{start}
	for _, d := range deltas {
		out = append(out, out[len(out)-1]+d)
	}
	return out
}

func TestBuildConstantRate(t *testing.T) {
	segs := Build(cum(0, 480, 480, 480, 480, 480), 3)
	if len(segs) != 1 {
		t.Fatalf("want 1 segment, got %d", len(segs))
	}
	s := segs[0]
	if s.StartPacket != 0 || s.EndPacket != 6 || s.FrameSamples != 480 {
		t.Fatalf("bad segment %+v", s)
	}
	if s.StartSample != 0 || s.EndSample != 6*480 {
		t.Fatalf("bad sample range %+v", s)
	}
}

func TestBuildVariableRate(t *testing.T) {
	// Five 480-sample frames then five 960-sample frames.
	s := cum(0, 480, 480, 480, 480, 480, 960, 960, 960, 960, 960)
	segs := Build(s, 2)
	if len(segs) != 2 {
		t.Fatalf("want 2 segments, got %d: %+v", len(segs), segs)
	}
	if segs[0].FrameSamples != 480 || segs[1].FrameSamples != 960 {
		t.Fatalf("frames %+v", segs)
	}
	// Boundary sample belongs to exactly one segment.
	if segs[0].EndSample != segs[1].StartSample {
		t.Fatalf("boundary mismatch: %+v", segs)
	}
	if segs[0].EndPacket != segs[1].StartPacket {
		t.Fatalf("packet boundary mismatch: %+v", segs)
	}
}

func TestShortRunMergesWithSource(t *testing.T) {
	// Long 480 run, one 960 blip, long 480 run.
	s := cum(0, 480, 480, 480, 480, 960, 480, 480, 480, 480)
	segs := Build(s, 3)
	for _, seg := range segs {
		if seg.Packets() < 3 {
			t.Fatalf("short segment survived: %+v", seg)
		}
	}
	var merged bool
	for _, seg := range segs {
		if seg.Merged && len(seg.Sources) > 0 {
			merged = true
		}
	}
	if !merged {
		t.Fatalf("merge source not recorded: %+v", segs)
	}
	// Full packet coverage, no overlap.
	if segs[0].StartPacket != 0 || segs[len(segs)-1].EndPacket != len(s) {
		t.Fatalf("packet coverage broken: %+v", segs)
	}
	for i := 1; i < len(segs); i++ {
		if segs[i].StartPacket != segs[i-1].EndPacket {
			t.Fatalf("segments overlap or gap: %+v", segs)
		}
	}
}

func TestGapRecordedNotFabricated(t *testing.T) {
	// One missing frame: delta of 2*480 between packets 2 and 3.
	s := cum(0, 480, 480, 960, 480, 480, 480)
	segs := Build(s, 2)
	var gaps int
	for _, seg := range segs {
		gaps += len(seg.Gaps)
	}
	if gaps == 0 {
		t.Fatalf("missing-sample gap not recorded: %+v", segs)
	}
	c := Coverage(segs)
	if c >= 1 || c <= 0 {
		t.Fatalf("coverage must exclude the gap: %f", c)
	}
}

func TestBuildSinglePacket(t *testing.T) {
	segs := Build([]uint64{42}, 3)
	if len(segs) != 1 || segs[0].Packets() != 1 {
		t.Fatalf("single packet: %+v", segs)
	}
}
