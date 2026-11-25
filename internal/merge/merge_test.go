package merge

import (
	"testing"

	"tracecheck/internal/model"
)

func TestMergeDeterministicOrder(t *testing.T) {
	rtpEvents := []model.TimelineEvent{
		{Kind: model.EventPacket, Start: 20_000, End: 40_000, Stream: 1, Sequence: 2},
		{Kind: model.EventPacket, Start: 0, End: 20_000, Stream: 1, Sequence: 1},
	}
	mp4Events := []model.TimelineEvent{
		{Kind: model.EventKeyframe, Start: 10_000, End: 10_001, Track: 2},
		{Kind: model.EventKeyframe, Start: 0, End: 1, Track: 2},
	}
	first := Merge(rtpEvents, mp4Events)
	second := Merge(mp4Events, rtpEvents)
	if len(first) != 4 || len(second) != 4 {
		t.Fatalf("merge lost events: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("merge not input-order independent at %d:\n%+v\n%+v", i, first[i], second[i])
		}
	}
	// First event is the earliest start; keyframe wins the start tie.
	if first[0].Kind != model.EventKeyframe || first[0].Start != 0 {
		t.Fatalf("ordering wrong: %+v", first[0])
	}
	if !Monotonic(first) {
		t.Fatal("merged timeline must be monotonic")
	}
}

func TestMergeEmpty(t *testing.T) {
	if got := Merge(nil, nil); got != nil {
		t.Fatalf("empty merge must be nil, got %+v", got)
	}
}

func TestMergeAnomaliesUnionsOverlaps(t *testing.T) {
	in := []Anomaly{
		{Span: model.NewSpan(100, 200), LossCount: 2, Reasons: []string{"loss"}},
		{Span: model.NewSpan(150, 300), FlowChanges: 1, Reasons: []string{"flow"}},
		{Span: model.NewSpan(400, 500), LossCount: 1, Reasons: []string{"loss"}},
	}
	out := MergeAnomalies(in)
	if len(out) != 2 {
		t.Fatalf("want 2 merged regions, got %+v", out)
	}
	if out[0].Span.Start != 100 || out[0].Span.End != 300 ||
		out[0].LossCount != 2 || out[0].FlowChanges != 1 {
		t.Fatalf("union wrong: %+v", out[0])
	}
}

func TestThroughputFixedWindows(t *testing.T) {
	samples := []ByteSample{
		{At: 0, Bytes: 100},
		{At: 500_000, Bytes: 100},
		{At: 999_000, Bytes: 100}, // same 1s window
		{At: 1_500_000, Bytes: 400},
	}
	windows := Throughput(samples, model.TicksFromSeconds(1))
	if len(windows) != 2 {
		t.Fatalf("want 2 windows, got %+v", windows)
	}
	if windows[0].Bytes != 300 || windows[0].BytesPerSec != 300 {
		t.Fatalf("first window wrong: %+v", windows[0])
	}
	if windows[1].Bytes != 400 || windows[1].BytesPerSec != 400 {
		t.Fatalf("second window wrong: %+v", windows[1])
	}
}

func TestFlowChangeDetection(t *testing.T) {
	windows := []FlowWindow{
		{Span: model.NewSpan(0, 1_000_000), BytesPerSec: 1000},
		{Span: model.NewSpan(1_000_000, 2_000_000), BytesPerSec: 400}, // -60%
		{Span: model.NewSpan(2_000_000, 3_000_000), BytesPerSec: 420}, // +5%, ignored
	}
	changes := FlowChange(windows, 0.3)
	if len(changes) != 1 || changes[0].FlowChanges != 1 {
		t.Fatalf("want one flow change, got %+v", changes)
	}
	if changes[0].Span.Start != 1_000_000 {
		t.Fatalf("flow change located in wrong window: %+v", changes[0])
	}
}
