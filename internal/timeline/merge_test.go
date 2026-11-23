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
	// 100 transmitted: 90 accepted, 4 duplicates, 6 lost.
	if err := CheckConservation("reorder", 90, 4, 6, 100); err != nil {
		t.Fatalf("balanced counters rejected: %v", err)
	}
	err := CheckConservation("reorder", 89, 4, 6, 100)
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
