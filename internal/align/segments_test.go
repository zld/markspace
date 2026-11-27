package align

import (
	"testing"

	"github.com/local/tracecheck/internal/clock"
	"github.com/local/tracecheck/internal/mkv"
	"github.com/local/tracecheck/internal/rtptrace"
)

func mkBlock(track uint64, off int64, absNS int64) *mkv.Block {
	return &mkv.Block{TrackNumber: track, SourceOffset: off, AbsTime: clock.Tick(absNS)}
}

func twoLayerTrace() *rtptrace.Trace {
	return &rtptrace.Trace{Stats: map[uint64]*rtptrace.Stats{
		1: {TrackNumber: 1, PacketCount: 4},
		2: {TrackNumber: 2, PacketCount: 4},
	}}
}

func TestCutAndAlignWithinThreshold(t *testing.T) {
	blocks := []*mkv.Block{
		mkBlock(1, 10, 0),
		mkBlock(2, 11, 5_000_000), // 5ms skew, under 10ms threshold
		mkBlock(1, 20, 1_000_000_000),
		mkBlock(2, 21, 1_002_000_000), // 2ms skew
	}
	cfg := Config{
		SegmentNS: 1_000_000_000,
		MaxSkewNS: 10_000_000,
		Layers:    []Layer{{Bitrate: 16000, TrackNumber: 1}, {Bitrate: 32000, TrackNumber: 2}},
	}
	res := Cut(blocks, twoLayerTrace(), cfg)
	if len(res.Segments) != 2 {
		t.Fatalf("segment count = %d", len(res.Segments))
	}
	if res.Aligned != 2 {
		t.Fatalf("aligned = %d, want 2; failed=%v", res.Aligned, res.FailedRanges)
	}
	if res.MaxSkewNS != clock.Tick(5_000_000) {
		t.Fatalf("max skew = %d", res.MaxSkewNS)
	}
	if res.Segments[0].LayerCoverage[1] != 1 || res.Segments[0].LayerCoverage[2] != 1 {
		t.Fatalf("coverage = %+v", res.Segments[0].LayerCoverage)
	}
}

func TestSkewExceedsThresholdFails(t *testing.T) {
	blocks := []*mkv.Block{
		mkBlock(1, 10, 0),
		mkBlock(2, 11, 50_000_000), // 50ms skew over 10ms threshold
		mkBlock(1, 20, 1_000_000_000),
		mkBlock(2, 21, 1_001_000_000),
	}
	cfg := Config{
		SegmentNS: 1_000_000_000,
		MaxSkewNS: 10_000_000,
		Layers:    []Layer{{Bitrate: 16000, TrackNumber: 1}, {Bitrate: 32000, TrackNumber: 2}},
	}
	res := Cut(blocks, twoLayerTrace(), cfg)
	if res.Aligned != 1 {
		t.Fatalf("aligned = %d, want 1", res.Aligned)
	}
	if len(res.FailedRanges) != 1 || res.FailedRanges[0][0] != 0 {
		t.Fatalf("failed ranges = %v", res.FailedRanges)
	}
	if res.Segments[0].OK || res.Segments[0].FailCode != clock.CodeAlignmentSkew {
		t.Fatalf("segment 0 should fail with skew: %+v", res.Segments[0])
	}
	if res.MaxSkewNS != clock.Tick(50_000_000) {
		t.Fatalf("max skew = %d", res.MaxSkewNS)
	}
}

func TestEmptyInput(t *testing.T) {
	res := Cut(nil, nil, Config{SegmentNS: 1_000_000_000})
	if len(res.Segments) != 0 || res.Aligned != 0 {
		t.Fatal("empty input must yield empty result")
	}
}

func TestSourceOffsetsPreserved(t *testing.T) {
	blocks := []*mkv.Block{
		mkBlock(1, 101, 0),
		mkBlock(1, 202, 100_000_000),
		mkBlock(1, 303, 2_000_000_000),
	}
	cfg := Config{SegmentNS: 1_000_000_000, MaxSkewNS: 1_000_000, Layers: []Layer{{TrackNumber: 1}}}
	res := Cut(blocks, nil, cfg)
	if len(res.Segments) != 3 {
		t.Fatalf("segments = %d", len(res.Segments))
	}
	if got := res.Segments[0].BlockSourceOffsets; len(got) != 2 || got[0] != 101 || got[1] != 202 {
		t.Fatalf("offsets = %v", got)
	}
}
