package artifact

import (
	"strings"
	"testing"

	"medialab/internal/pixel"
)

func summaryFrames() []Frame {
	base := pixel.Stats{MeanR: 10, MeanG: 20, MeanB: 30, Variance: 100}
	return []Frame{
		{SHA256: strings.Repeat("a", 64), Bytes: 100, Stats: base, LoudnessDBFS: -6},
		{SHA256: strings.Repeat("b", 64), Bytes: 200, Stats: base, LoudnessDBFS: -12, Fill: true},
	}
}

func TestSummarizeAggregates(t *testing.T) {
	frames := summaryFrames()
	segs := []Segment{{SHA256: strings.Repeat("c", 64), Bytes: 50}}
	sum := Summarize(frames, segs)
	if sum.Tracks != 1 || sum.Frames != 2 || sum.FillFrames != 1 {
		t.Fatalf("counts = %+v", sum)
	}
	if sum.TotalBytes != 350 {
		t.Fatalf("bytes = %d want 350 (frames plus segment)", sum.TotalBytes)
	}
	if sum.MeanR != 10 || sum.MeanG != 20 || sum.MeanB != 30 || sum.Variance != 100 {
		t.Fatalf("stats = %+v", sum)
	}
	if sum.PeakLoudnessDBFS != -6 {
		t.Fatalf("peak loudness = %f", sum.PeakLoudnessDBFS)
	}
	if sum.RunHash == "" {
		t.Fatal("run hash empty")
	}
}

func TestSummarizeEmpty(t *testing.T) {
	sum := Summarize(nil, nil)
	if sum.Frames != 0 || sum.FillFrames != 0 {
		t.Fatalf("empty summary = %+v", sum)
	}
	if sum.PeakLoudnessDBFS != pixel.LoudnessFloorDBFS {
		t.Fatalf("loudness floor = %f", sum.PeakLoudnessDBFS)
	}
	// The run hash is still defined so an empty rerun is comparable.
	if sum.RunHash == "" {
		t.Fatal("empty run hash")
	}
}

func TestRunHashOrderSensitive(t *testing.T) {
	frames := summaryFrames()
	segs := []Segment{{SHA256: strings.Repeat("c", 64)}}
	forward := RunHash(frames, segs)
	reversed := RunHash([]Frame{frames[1], frames[0]}, segs)
	if forward == reversed {
		t.Fatal("run hash ignores frame order")
	}
	// Stable input must be stable.
	if RunHash(frames, segs) != forward {
		t.Fatal("run hash is not deterministic")
	}
}

func TestSummarizeDeterministic(t *testing.T) {
	if Summarize(summaryFrames(), nil) != Summarize(summaryFrames(), nil) {
		t.Fatal("summary is not deterministic")
	}
}
