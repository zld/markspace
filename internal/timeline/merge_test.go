package timeline

import (
	"testing"

	"mediaroute/internal/clock"
	"mediaroute/internal/codec"
	"mediaroute/internal/transport"
)

func frame(ssrc uint32, ts uint32, marker bool, complete bool, payload []byte) *transport.Frame {
	f := &transport.Frame{SSRC: ssrc, Timestamp: ts, Bytes: payload, Complete: complete}
	_ = marker
	return f
}

func TestMergeOrdersAcrossTracks(t *testing.T) {
	tc1 := NewTrackClock(100, codec.H264)
	tc2 := NewTrackClock(200, codec.H265)
	// 90kHz ticks: frame A at 90000 (1s), B at 45000 (0.5s).
	in1 := &Input{Track: tc1, Codec: codec.H264, SegmentID: "seg-1",
		Frames: []*transport.Frame{frame(100, 90_000, true, true, []byte{1})}}
	in2 := &Input{Track: tc2, Codec: codec.H265, SegmentID: "seg-1",
		Frames: []*transport.Frame{frame(200, 45_000, true, true, []byte{2})}}
	tl, excluded, err := Merge([]*Input{in1, in2})
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Frames) != 2 || len(excluded) != 0 {
		t.Fatalf("merge counts wrong: %d frames %d excluded", len(tl.Frames), len(excluded))
	}
	if tl.Frames[0].Track != 200 || tl.Frames[0].Usec != 500_000 {
		t.Fatalf("earlier frame not first: %+v", tl.Frames[0])
	}
	if tl.Frames[1].Usec != 1_000_000 {
		t.Fatalf("second frame mapping wrong: %+v", tl.Frames[1])
	}
}

func TestMergeTimestampWraparound(t *testing.T) {
	tc := NewTrackClock(100, codec.H264)
	in := &Input{Track: tc, Codec: codec.H264, SegmentID: "seg-1", Frames: []*transport.Frame{
		frame(100, 0xFFFFFFD2, true, true, []byte{1}), // -46 as uint32
		frame(100, 10, true, true, []byte{2}),         // wraps forward by 56 ticks
	}}
	tl, _, err := Merge([]*Input{in})
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Frames) != 2 {
		t.Fatalf("frame count = %d", len(tl.Frames))
	}
	if tl.Frames[1].Usec <= tl.Frames[0].Usec {
		t.Fatalf("wrap did not preserve monotonic order: %d -> %d",
			tl.Frames[0].Usec, tl.Frames[1].Usec)
	}
	delta := tl.Frames[1].Usec - tl.Frames[0].Usec
	// 56 ticks at 90kHz -> 622.22us, integer mapped to 622.
	if delta != 622 {
		t.Fatalf("wrap delta = %d want 622", delta)
	}
}

func TestMergeExcludesTruncated(t *testing.T) {
	tc := NewTrackClock(100, codec.AV1)
	in := &Input{Track: tc, Codec: codec.AV1, SegmentID: "seg-2", Frames: []*transport.Frame{
		frame(100, 90_000, true, true, []byte{0x12, 0x00}),
		frame(100, 180_000, false, false, []byte{0x12, 0x01}),
	}}
	tl, excluded, err := Merge([]*Input{in})
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Frames) != 1 || len(excluded) != 1 {
		t.Fatalf("truncated frame handling wrong: merged=%d excluded=%d",
			len(tl.Frames), len(excluded))
	}
	if excluded[0].Complete || excluded[0].SegmentID != "seg-2" {
		t.Fatalf("excluded evidence wrong: %+v", excluded[0])
	}
}

func TestMergeKeyframeCallback(t *testing.T) {
	tc := NewTrackClock(100, codec.H264)
	idrb := []byte{0, 0, 0, 1, 0x65, 1, 2, 3, 0, 0, 1}
	pb := []byte{0, 0, 0, 1, 0x41, 9, 0, 0, 1}
	in := &Input{Track: tc, Codec: codec.H264, SegmentID: "seg-1", Frames: []*transport.Frame{
		frame(100, 90_000, true, true, idrb),
		frame(100, 180_000, true, true, pb),
	}, Keyframe: func(f *transport.Frame) (bool, error) {
		return codec.FrameKey(codec.H264, f.Bytes)
	}}
	tl, _, err := Merge([]*Input{in})
	if err != nil {
		t.Fatal(err)
	}
	if !tl.Frames[0].Keyframe || tl.Frames[1].Keyframe {
		t.Fatalf("keyframe flags wrong: %+v %+v", tl.Frames[0], tl.Frames[1])
	}
}

func TestConservationCheck(t *testing.T) {
	// 100 distinct transmitted: 94 accepted, 6 lost; duplicates are extra.
	if err := CheckConservation("reorder", 94, 4, 6, 100); err != nil {
		t.Fatalf("balanced counters rejected: %v", err)
	}
	err := CheckConservation("reorder", 93, 4, 6, 100)
	if err == nil {
		t.Fatal("imbalanced counters accepted")
	}
	if ce, ok := err.(*ConservationError); !ok || ce.Expected != 100 || ce.Actual != 99 {
		t.Fatalf("conservation error detail wrong: %v", err)
	}
}

func TestUnifiedClockRejectsBadRate(t *testing.T) {
	tc := NewTrackClock(100, codec.H264)
	tc.Rate = clock.Rate{Num: 1, Den: 0}
	in := &Input{Track: tc, Codec: codec.H264, SegmentID: "x",
		Frames: []*transport.Frame{frame(100, 1, true, true, nil)}}
	if _, _, err := Merge([]*Input{in}); err == nil {
		t.Fatal("zero denominator rate accepted in timeline merge")
	}
}

// TestMergeKeepsArrivalOrderPerTrack guards the clock contract: frames must
// be merged in the order the transport stage emitted them, even when a
// truncated frame sits between complete ones. Feeding a reordered slice would
// advance Unwrap32 backwards and corrupt every later timestamp.
func TestMergeKeepsArrivalOrderPerTrack(t *testing.T) {
	tc := NewTrackClock(100, codec.H264)
	in := &Input{Track: tc, Codec: codec.H264, SegmentIDs: []string{"seg-0", "", "seg-0"},
		Frames: []*transport.Frame{
			frame(100, 90_000, true, true, []byte{1}),
			frame(100, 180_000, false, false, []byte{2}), // truncated, no segment
			frame(100, 270_000, true, true, []byte{3}),
		}}
	tl, excluded, err := Merge([]*Input{in})
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Frames) != 2 || len(excluded) != 1 {
		t.Fatalf("merge counts wrong: %d merged %d excluded",
			len(tl.Frames), len(excluded))
	}
	if tl.Frames[0].Usec != 1_000_000 || tl.Frames[1].Usec != 3_000_000 {
		t.Fatalf("arrival order not preserved: %d then %d",
			tl.Frames[0].Usec, tl.Frames[1].Usec)
	}
	if tl.Frames[1].SegmentID != "seg-0" {
		t.Fatalf("segment id lost on arrival-ordered merge: %+v", tl.Frames[1])
	}
}

// TestMergeWrapsFromNonZeroOrigin covers the realistic wrap shape: the stream
// starts at a small non-zero timestamp, then crosses 2^32. The 32-bit base
// must be rebased so the post-wrap frames continue forward in unified time
// instead of reading as a huge backward jump.
func TestMergeWrapsFromNonZeroOrigin(t *testing.T) {
	tc := NewTrackClock(30, codec.AV1)
	in := &Input{Track: tc, Codec: codec.AV1, SegmentID: "seg-1", Frames: []*transport.Frame{
		frame(30, ^uint32(0)-6006, true, true, []byte{1}),
		frame(30, ^uint32(0)-3003, true, true, []byte{2}),
		frame(30, ^uint32(0), true, true, []byte{3}), // last pre-wrap tick
		frame(30, 3002, true, true, []byte{4}),       // wrapped forward
		frame(30, 6005, true, true, []byte{5}),
	}}
	tl, _, err := Merge([]*Input{in})
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Frames) != 5 {
		t.Fatalf("frame count = %d", len(tl.Frames))
	}
	for i := 1; i < len(tl.Frames); i++ {
		if tl.Frames[i].Usec <= tl.Frames[i-1].Usec {
			t.Fatalf("wrap broke monotonic order at %d: %d -> %d", i,
				tl.Frames[i-1].Usec, tl.Frames[i].Usec)
		}
	}
	// Exactly one 90kHz tick spans the wrap boundary (3003 ticks ~ 33367us).
	if delta := tl.Frames[3].Usec - tl.Frames[2].Usec; delta != 33367 {
		t.Fatalf("post-wrap tick delta = %d want 33367us", delta)
	}
}

// TestMergeRejectsBackwardClock ensures a mid-stream backward jump is an
// error with timeline context rather than a silently shifted timeline.
func TestMergeRejectsBackwardClock(t *testing.T) {
	tc := NewTrackClock(30, codec.AV1)
	in := &Input{Track: tc, Codec: codec.AV1, SegmentID: "seg-1", Frames: []*transport.Frame{
		frame(30, 900_000, true, true, []byte{1}),
		frame(30, 10, true, true, []byte{2}),
	}}
	_, _, err := Merge([]*Input{in})
	if err == nil {
		t.Fatal("backward timestamp accepted")
	}
	pe, ok := clock.AsError(err)
	if !ok || pe.Recoverable || pe.Phase != clock.PhaseTimeline {
		t.Fatalf("backward clock error model wrong: %v", err)
	}
}
