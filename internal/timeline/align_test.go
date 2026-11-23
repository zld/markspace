package timeline

import (
	"errors"
	"testing"
)

func nsClock(t *testing.T, scale int64) *TrackClock {
	t.Helper()
	c, err := NewTrackClock(uint64(scale), 33_333_333, 0)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestClockMonotonic(t *testing.T) {
	c := nsClock(t, 1_000_000)
	for i, tick := range []int64{0, 33, 66, 100} {
		got, err := c.Observe(tick, int64(i*10))
		if err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
		if want := tick * 1_000_000; got != want {
			t.Fatalf("tick %d => %d, want %d", tick, got, want)
		}
	}
	if len(c.Issues()) != 0 {
		t.Fatalf("unexpected issues: %v", c.Issues())
	}
}

func TestClockReverseRejected(t *testing.T) {
	c := nsClock(t, 1_000_000)
	if _, err := c.Observe(100, 10); err != nil {
		t.Fatal(err)
	}
	_, err := c.Observe(90, 20)
	var issue Issue
	if !errors.As(err, &issue) {
		t.Fatalf("expected Issue, got %v", err)
	}
	if issue.Kind != IssueTimestampReverse {
		t.Fatalf("kind = %s", issue.Kind)
	}
	if issue.Offset != 20 || issue.Track != 0 {
		t.Fatalf("issue provenance lost: %+v", issue)
	}
}

func TestClockWrapDetected(t *testing.T) {
	c, err := NewTrackClock(1_000_000, 33_333_333, 0)
	if err != nil {
		t.Fatal(err)
	}
	c.WrapLimit = 1 << 32
	if _, err := c.Observe(1<<32-100, 0); err != nil {
		t.Fatal(err)
	}
	_, err = c.Observe(50, 1)
	var issue Issue
	if !errors.As(err, &issue) || issue.Kind != IssueTimestampWrap {
		t.Fatalf("err = %v", err)
	}
}

func TestClockSparseTrack(t *testing.T) {
	c := nsClock(t, 1_000_000)
	if _, err := c.Observe(0, 0); err != nil {
		t.Fatal(err)
	}
	// A 1s gap against a 33ms frame is a sparse-track warning, not a hard
	// rejection: the block is still usable, but the anomaly is recorded.
	if _, err := c.Observe(1000, 1); err != nil {
		t.Fatalf("sparse track must not be fatal: %v", err)
	}
	issues := c.Issues()
	if len(issues) != 1 || issues[0].Kind != IssueSparseTrack {
		t.Fatalf("issues = %+v", issues)
	}
	if issues[0].Offset != 1 {
		t.Fatalf("sparse issue lost provenance: %+v", issues[0])
	}
}

func TestClockOverflow(t *testing.T) {
	c, err := NewClock(1 << 40)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ToNanos(1 << 30); err == nil {
		t.Fatal("expected overflow")
	}
}

func keyframes(start, frame, count int64) []int64 {
	out := make([]int64, count)
	for i := range out {
		out[i] = start + int64(i)*frame
	}
	return out
}

func TestAlignThreeRepresentations(t *testing.T) {
	// Keyframe intervals differ: 2s for the low rendition, 1s for others.
	align, err := Align(AlignInput{
		Representations: []Representation{
			{ID: "360p", Bandwidth: 400_000, Frames: keyframes(0, 2e9, 6), Duration: 10e9},
			{ID: "720p", Bandwidth: 1_200_000, Frames: keyframes(0, 1e9, 10), Duration: 10e9},
			{ID: "1080p", Bandwidth: 3_000_000, Frames: keyframes(0, 1e9, 10), Duration: 10e9},
		},
		TargetDuration: 4e9,
		Tolerance:      33_333_333,
	})
	if err != nil {
		t.Fatalf("Align: %v", err)
	}
	want := []int64{0, 4e9, 8e9}
	if len(align.Cuts) != len(want) {
		t.Fatalf("cuts = %v", align.Cuts)
	}
	for i := range want {
		if align.Cuts[i] != want[i] {
			t.Fatalf("cut %d = %d, want %d", i, align.Cuts[i], want[i])
		}
	}
	for id, idx := range align.PerRepresentation {
		if len(idx) != len(want) {
			t.Fatalf("%s index length %d", id, len(idx))
		}
	}
	if seg := align.SegmentOf(5e9); seg != 1 {
		t.Fatalf("SegmentOf(5e9) = %d", seg)
	}
}

func TestAlignRejectsMisalignedRepresentation(t *testing.T) {
	_, err := Align(AlignInput{
		Representations: []Representation{
			{ID: "a", Bandwidth: 100, Frames: []int64{0, 2e9, 4e9, 6e9}, Duration: 8e9},
			{ID: "b", Bandwidth: 200, Frames: []int64{0, 3_500_000_000, 7e9}, Duration: 8e9},
		},
		TargetDuration: 2e9,
		Tolerance:      33_333_333,
	})
	var ae *AlignError
	if !errors.As(err, &ae) {
		t.Fatalf("expected AlignError, got %v", err)
	}
	if ae.Representation == "" {
		t.Fatalf("missing representation in error: %+v", ae)
	}
}

func TestAlignToleranceBoundary(t *testing.T) {
	// Keyframe 10ms from the grid is acceptable at 33ms tolerance, but 40ms
	// is not: the cut grid must not drift by more than one frame.
	framesA := []int64{0, 1e9, 2e9, 3e9}
	framesB := []int64{0, 1_010_000_000, 2e9, 3_010_000_000}
	if _, err := Align(AlignInput{
		Representations: []Representation{
			{ID: "a", Bandwidth: 100, Frames: framesA, Duration: 4e9},
			{ID: "b", Bandwidth: 200, Frames: framesB, Duration: 4e9},
		},
		TargetDuration: 1e9,
		Tolerance:      33_333_333,
	}); err != nil {
		t.Fatalf("within tolerance rejected: %v", err)
	}
	if _, err := Align(AlignInput{
		Representations: []Representation{
			{ID: "a", Bandwidth: 100, Frames: framesA, Duration: 4e9},
			{ID: "b", Bandwidth: 200, Frames: []int64{0, 1_040_000_000, 2e9, 3e9}, Duration: 4e9},
		},
		TargetDuration: 1e9,
		Tolerance:      33_333_333,
	}); err == nil {
		t.Fatal("beyond tolerance accepted")
	}
}

func TestAlignDeterministic(t *testing.T) {
	in := AlignInput{
		Representations: []Representation{
			{ID: "z", Bandwidth: 100, Frames: keyframes(0, 2e9, 6), Duration: 10e9},
			{ID: "a", Bandwidth: 200, Frames: keyframes(0, 1e9, 10), Duration: 10e9},
			{ID: "m", Bandwidth: 300, Frames: keyframes(0, 1e9, 10), Duration: 10e9},
		},
		TargetDuration: 3e9,
		Tolerance:      33_333_333,
	}
	first, err := Align(in)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		got, err := Align(in)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Cuts) != len(first.Cuts) {
			t.Fatal("non-deterministic cuts")
		}
		for j := range got.Cuts {
			if got.Cuts[j] != first.Cuts[j] {
				t.Fatalf("cut drift at %d: %d != %d", j, got.Cuts[j], first.Cuts[j])
			}
		}
	}
}

func TestAlignInputValidation(t *testing.T) {
	if _, err := Align(AlignInput{TargetDuration: 1}); err == nil {
		t.Fatal("expected error for too few representations")
	}
	_, err := Align(AlignInput{
		Representations: []Representation{{ID: "a", Frames: []int64{0}}, {ID: "b", Frames: []int64{0}}},
		TargetDuration:  0,
	})
	if err == nil {
		t.Fatal("expected error for non-positive duration")
	}
}
