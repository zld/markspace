package align_test

import (
	"errors"
	"testing"

	"mediacheck/internal/align"
	"mediacheck/internal/timeline"
)

// buildRep makes a representation: keyframes every keyEvery frames, with the
// given per-frame nanosecond deltas (VFR) and an optional constant T0 offset.
func buildRep(id string, deltas []int64, keyEvery int, offset int64) align.Representation {
	frames := make([]timeline.Frame, len(deltas)+1)
	t := offset
	frames[0] = timeline.Frame{TrackNumber: 1, T0: t, Keyframe: true}
	for i, d := range deltas {
		t += d
		frames[i+1] = timeline.Frame{
			TrackNumber: 1, T0: t, Keyframe: (i+1)%keyEvery == 0,
		}
	}
	return align.Representation{ID: id, Frames: frames}
}

func TestAlignedRepresentationsShareBoundaries(t *testing.T) {
	// Three bitrates, CFR 40 ms, keyframes every 25 frames (1 s), tiny start skew.
	deltas := make([]int64, 99)
	for i := range deltas {
		deltas[i] = 40_000_000
	}
	reps := []align.Representation{
		buildRep("r0", deltas, 25, 0),
		buildRep("r1", deltas, 25, 5_000_000),
		buildRep("r2", deltas, 25, 12_000_000),
	}
	a, err := align.New(align.Options{TargetSegmentNS: 1_000_000_000, MaxSkewNS: 20_000_000})
	if err != nil {
		t.Fatal(err)
	}
	bounds, err := a.Align(reps)
	if err != nil {
		t.Fatalf("alignment failed: %v", err)
	}
	if len(bounds) < 3 {
		t.Fatalf("boundaries=%d want >=3", len(bounds))
	}
	// Every boundary must resolve to a frame index in every representation.
	for _, b := range bounds {
		if len(b.FrameIndex) != 3 {
			t.Fatalf("boundary missing representations: %+v", b)
		}
	}
	// Segments are contiguous and strictly positive (VFR makes them unequal).
	durs := align.SegmentDurations(bounds, reps[0].Frames[len(reps[0].Frames)-1].T0)
	for i, d := range durs {
		if d <= 0 {
			t.Fatalf("non-positive segment %d duration %d", i, d)
		}
	}
	// Boundary times themselves must be strictly increasing.
	for i := 1; i < len(bounds); i++ {
		if bounds[i].T0 <= bounds[i-1].T0 {
			t.Fatalf("boundary time not increasing at %d", i)
		}
	}
}

func TestVFROverDriftStillAlignsOnKeyframes(t *testing.T) {
	// VFR jitter (33/47 ms alternating) plus a fixed drift offset between
	// layers; keyframes still coincide within tolerance.
	mkDeltas := func(scale float64) []int64 {
		d := make([]int64, 119)
		for i := range d {
			base := int64(40_000_000)
			if i%2 == 0 {
				base -= 7_000_000
			} else {
				base += 7_000_000
			}
			d[i] = int64(float64(base) * scale)
		}
		return d
	}
	reps := []align.Representation{
		buildRep("vfr-a", mkDeltas(1.0), 30, 0),
		buildRep("vfr-b", mkDeltas(1.00008), 30, 0), // 80 ppm drift
	}
	a, _ := align.New(align.Options{TargetSegmentNS: 1_200_000_000, MaxSkewNS: 30_000_000})
	bounds, err := a.Align(reps)
	if err != nil {
		t.Fatalf("VFR+drift alignment should converge: %v", err)
	}
	if len(bounds) < 2 {
		t.Fatalf("too few boundaries: %d", len(bounds))
	}
}

func TestMissingCommonKeyframeFailsClosed(t *testing.T) {
	// r0 has a keyframe grid; r1 deliberately drops the keyframe near 1 s so
	// no common boundary exists inside the tight skew window.
	d0 := make([]int64, 99)
	for i := range d0 {
		d0[i] = 40_000_000
	}
	r0 := buildRep("good", d0, 25, 0)

	d1 := make([]int64, 99)
	for i := range d1 {
		d1[i] = 40_000_000
	}
	frames := make([]timeline.Frame, 100)
	cur := int64(0)
	for i := range frames {
		frames[i] = timeline.Frame{T0: cur, Keyframe: i%25 == 0}
		// Suppress the keyframe that would sit near t=1s.
		if i == 25 {
			frames[i].Keyframe = false
		}
		cur += 40_000_000
	}
	r1 := align.Representation{ID: "broken", Frames: frames}

	a, _ := align.New(align.Options{TargetSegmentNS: 1_000_000_000, MaxSkewNS: 10_000_000})
	_, err := a.Align([]align.Representation{r0, r1})
	var me *align.MisalignError
	if !errors.As(err, &me) {
		t.Fatalf("want MisalignError, got %v", err)
	}
	if me.Track != "broken" || len(me.Candidates) == 0 {
		t.Fatalf("error must name track and candidates: %v", me)
	}
}

func TestNoKeyframeAtAllRejected(t *testing.T) {
	frames := []timeline.Frame{{T0: 0}, {T0: 40_000_000}}
	a, _ := align.New(align.Options{TargetSegmentNS: 1_000_000_000, MaxSkewNS: 20_000_000})
	_, err := a.Align([]align.Representation{{ID: "x", Frames: frames}})
	var me *align.MisalignError
	if !errors.As(err, &me) || me.Reason != "no keyframe" {
		t.Fatalf("want no-keyframe error, got %v", err)
	}
}

func TestDeterministicBoundaryOrder(t *testing.T) {
	deltas := make([]int64, 124)
	for i := range deltas {
		deltas[i] = 40_000_000
	}
	a, _ := align.New(align.Options{TargetSegmentNS: 1_000_000_000, MaxSkewNS: 20_000_000})
	first, err := a.Align([]align.Representation{buildRep("r", deltas, 25, 0)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Align([]align.Representation{buildRep("r", deltas, 25, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) {
		t.Fatal("non-deterministic boundary count")
	}
	for i := range first {
		if first[i].T0 != second[i].T0 {
			t.Fatalf("boundary %d moved: %d != %d", i, first[i].T0, second[i].T0)
		}
	}
}
