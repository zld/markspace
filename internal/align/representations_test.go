package align

import (
	"testing"

	"github.com/zld/avpipe/internal/mp4"
)

func repWith(rep string, scale uint32, seqs ...uint32) *mp4.Report {
	r := &mp4.Report{Representation: rep, Timescale: scale}
	for _, seq := range seqs {
		twoSeconds := int64(2 * scale)
		start := int64(seq-1) * twoSeconds
		r.Segments = append(r.Segments, mp4.Segment{
			Representation: rep, Sequence: seq, SequenceKnown: true,
			StartTicks: start, DurationTicks: uint32(twoSeconds),
			Timescale: scale, TimingKnown: true, Readable: true,
		})
	}
	return r
}

func TestAlignsCommonBoundariesAcrossTimescales(t *testing.T) {
	// DurationTicks calculation above is integer only for scales dividing 90000.
	low := repWith("low", 90000, 1, 2, 3)
	mid := repWith("mid", 1000, 1, 2, 3)
	high := repWith("high", 48000, 1, 2, 3)
	// Correct 48k 2s duration and starts explicitly.
	for i := range high.Segments {
		high.Segments[i].StartTicks = int64(i) * 96000
		high.Segments[i].DurationTicks = 96000
	}

	got := Align([]*mp4.Report{low, mid, high}, 1000)
	if len(got.Errors) != 0 {
		t.Fatalf("unexpected alignment errors: %+v", got.Errors)
	}
	if len(got.Segments) != 3 {
		t.Fatalf("segments=%d want 3", len(got.Segments))
	}
	if got.Segments[1].StartMillis != 2000 || got.Segments[1].DurationMillis != 2000 {
		t.Fatalf("segment 2 timing=%d/%d want 2000/2000", got.Segments[1].StartMillis, got.Segments[1].DurationMillis)
	}
	if len(got.Segments[0].Representations) != 3 {
		t.Fatalf("representations=%v", got.Segments[0].Representations)
	}
}

func TestMissingBoundaryReportsErrorAndOmitsEntry(t *testing.T) {
	a := repWith("a", 90000, 1, 2)
	b := repWith("b", 90000, 1)
	got := Align([]*mp4.Report{a, b}, 1000)
	if len(got.Segments) != 1 {
		t.Fatalf("segments=%d want only common sequence 1", len(got.Segments))
	}
	if len(got.Errors) != 1 || got.Errors[0].Code != "missing_boundary" || got.Errors[0].Sequence != 2 {
		t.Fatalf("errors=%+v", got.Errors)
	}
	if len(got.Errors[0].MissingRepresentations) != 1 || got.Errors[0].MissingRepresentations[0] != "b" {
		t.Fatalf("missing reps=%+v", got.Errors[0].MissingRepresentations)
	}
}

func TestMismatchedBoundaryNotSilentlyShifted(t *testing.T) {
	a := repWith("a", 90000, 1, 2)
	b := repWith("b", 90000, 1, 2)
	b.Segments[1].StartTicks++ // one tick错位
	got := Align([]*mp4.Report{a, b}, 1000)
	if len(got.Segments) != 1 {
		t.Fatalf("segments=%d want mismatched sequence omitted", len(got.Segments))
	}
	if len(got.Errors) != 1 || got.Errors[0].Code != "boundary_mismatch" {
		t.Fatalf("errors=%+v", got.Errors)
	}
}
