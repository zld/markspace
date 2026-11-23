package segment

import (
	"testing"
	"time"

	"avbatch/internal/mediaerr"
)

const ms = int64(time.Millisecond)

// vfrSamples builds N samples starting at start, with per-index durations
// taken from durs (in ms), so frame rate can vary arbitrarily.
func vfrSamples(start int64, durs []int64) []Sample {
	out := make([]Sample, len(durs))
	t := start
	for i, d := range durs {
		out[i] = Sample{
			Ordinal:  i / 4,
			Index:    i,
			Nanos:    t,
			Duration: d * ms,
			Size:     1000 + i,
		}
		t += d * ms
	}
	return out
}

func TestCutConstantFrameRate(t *testing.T) {
	c, err := New(Config{SegmentNanos: 2000 * ms, BoundarySearchNanos: 500 * ms})
	if err != nil {
		t.Fatal(err)
	}
	durs := make([]int64, 60)
	for i := range durs {
		durs[i] = 40 // 25 fps
	}
	man, err := c.Cut(vfrSamples(0, durs))
	if err != nil {
		t.Fatalf("cut: %v", err)
	}
	if err := ValidateManifest(man); err != nil {
		t.Fatalf("manifest invalid: %v", err)
	}
	if man.Stats.Segments < 2 {
		t.Fatalf("segments = %d, want at least 2", man.Stats.Segments)
	}
	if man.Stats.OutputSamples != 60 {
		t.Fatalf("samples = %d", man.Stats.OutputSamples)
	}
	if man.Gaps != 0 {
		t.Fatalf("unexpected gaps: %d", man.Gaps)
	}
	if man.Stats.AlignmentErrorNanos > 500*ms {
		t.Fatalf("alignment error %d ns outside search window", man.Stats.AlignmentErrorNanos)
	}
}

func TestCutVFRBoundariesLandOnSamples(t *testing.T) {
	c, err := New(Config{SegmentNanos: 1000 * ms, BoundarySearchNanos: 300 * ms})
	if err != nil {
		t.Fatal(err)
	}
	durs := []int64{}
	// VFR: 20, 50, 33, 16 ms repeating, plus a 400 ms hold.
	for i := 0; i < 80; i++ {
		switch i % 9 {
		case 0:
			durs = append(durs, 20)
		case 1:
			durs = append(durs, 50)
		case 2:
			durs = append(durs, 33)
		case 3:
			durs = append(durs, 400)
		default:
			durs = append(durs, 16)
		}
	}
	samples := vfrSamples(0, durs)
	man, err := c.Cut(samples)
	if err != nil {
		t.Fatalf("cut: %v", err)
	}
	if err := ValidateManifest(man); err != nil {
		t.Fatalf("manifest invalid: %v", err)
	}
	startSet := map[int64]bool{}
	for _, s := range samples {
		startSet[s.Nanos] = true
	}
	for _, seg := range man.Segments {
		if seg.Kind == KindSegment && !startSet[seg.Start] {
			t.Fatalf("segment %d starts at %d, not a sample start", seg.Index, seg.Start)
		}
	}
	// No two segment intervals may overlap.
	for i := 1; i < len(man.Segments); i++ {
		if man.Segments[i].Start < man.Segments[i-1].End {
			t.Fatalf("overlap between entries %d and %d", i-1, i)
		}
	}
}

func TestCutReportsGapForMissingFragment(t *testing.T) {
	c, err := New(Config{SegmentNanos: 1000 * ms, BoundarySearchNanos: 200 * ms})
	if err != nil {
		t.Fatal(err)
	}
	// Two islands of samples with a 1.5 s hole between them.
	var samples []Sample
	tm := int64(0)
	for i := 0; i < 20; i++ {
		samples = append(samples, Sample{Ordinal: 0, Index: i, Nanos: tm, Duration: 50 * ms, Size: 500})
		tm += 50 * ms
	}
	holeStart := tm
	tm += 1500 * ms
	for i := 0; i < 20; i++ {
		samples = append(samples, Sample{Ordinal: 2, Index: i, Nanos: tm, Duration: 50 * ms, Size: 500})
		tm += 50 * ms
	}
	man, err := c.Cut(samples)
	if err != nil {
		t.Fatalf("cut: %v", err)
	}
	if man.Gaps == 0 {
		t.Fatal("missing fragment was not reported as a gap")
	}
	foundGap := false
	for _, s := range man.Segments {
		if s.Kind == KindGap && s.Start >= holeStart {
			foundGap = true
		}
	}
	if !foundGap {
		t.Fatalf("gap entries: %+v", man.Segments)
	}
	// Provenance must survive: ordinals 0 and 2 only.
	seen := map[int]bool{}
	for _, s := range man.Segments {
		for _, o := range s.SourceOrdinals {
			seen[o] = true
		}
	}
	if len(seen) != 2 || !seen[0] || !seen[2] {
		t.Fatalf("source ordinals = %v", seen)
	}
}

func TestCutRefusesOverlap(t *testing.T) {
	c, _ := New(Config{SegmentNanos: 1000 * ms})
	samples := []Sample{
		{Nanos: 0, Duration: 500 * ms, Size: 10},
		{Nanos: 100 * ms, Duration: 50 * ms, Size: 10},
	}
	_, err := c.Cut(samples)
	if !mediaerr.Is(err, mediaerr.KindOverlap) {
		t.Fatalf("err = %v, want overlap", err)
	}
}

func TestCutRefusesAmbiguousSamples(t *testing.T) {
	c, _ := New(Config{SegmentNanos: 1000 * ms})
	samples := []Sample{
		{Nanos: 0, Duration: 10 * ms, UncertaintyNanos: 6 * ms, Size: 10},
		{Nanos: 8 * ms, Duration: 10 * ms, UncertaintyNanos: 6 * ms, Size: 10},
	}
	_, err := c.Cut(samples)
	if !mediaerr.Is(err, mediaerr.KindUnsorted) {
		t.Fatalf("err = %v, want unsorted", err)
	}
}

func TestCutDeterministic(t *testing.T) {
	c, _ := New(Config{SegmentNanos: 1000 * ms, BoundarySearchNanos: 300 * ms, PreferKeyframes: true})
	durs := make([]int64, 40)
	for i := range durs {
		durs[i] = 33
	}
	samples := vfrSamples(0, durs)
	samples[12].Keyframe = true
	samples[24].Keyframe = true
	a, _ := c.Cut(samples)
	b, _ := c.Cut(samples)
	if len(a.Segments) != len(b.Segments) {
		t.Fatal("manifest length differs between runs")
	}
	for i := range a.Segments {
		x, y := a.Segments[i], b.Segments[i]
		if x.Index != y.Index || x.Kind != y.Kind || x.Start != y.Start || x.End != y.End ||
			x.SampleCount != y.SampleCount || x.Bytes != y.Bytes || x.Keyframe != y.Keyframe ||
			len(x.SourceOrdinals) != len(y.SourceOrdinals) {
			t.Fatalf("segment %d differs between runs: %+v vs %+v", i, x, y)
		}
		for j := range x.SourceOrdinals {
			if x.SourceOrdinals[j] != y.SourceOrdinals[j] {
				t.Fatalf("segment %d provenance differs", i)
			}
		}
	}
}

func TestCutPrefersKeyframe(t *testing.T) {
	c, _ := New(Config{SegmentNanos: 1000 * ms, BoundarySearchNanos: 400 * ms, PreferKeyframes: true})
	var samples []Sample
	tm := int64(0)
	for i := 0; i < 40; i++ {
		s := Sample{Nanos: tm, Duration: 50 * ms, Size: 100}
		// Keyframe slightly further from the 1s target than a plain sample.
		if i == 21 { // 1050ms
			s.Keyframe = true
		}
		samples = append(samples, s)
		tm += 50 * ms
	}
	man, err := c.Cut(samples)
	if err != nil {
		t.Fatalf("cut: %v", err)
	}
	if len(man.Segments) < 2 {
		t.Fatalf("segments = %d", len(man.Segments))
	}
	if !man.Segments[1].Keyframe {
		t.Fatalf("second segment did not start on a keyframe: %+v", man.Segments[1])
	}
}

func TestCutNoLegalBoundaryReported(t *testing.T) {
	// Two-second frames against a one-second target: the only candidate for
	// the second boundary is the sample already used for the first, so the
	// target cannot be honoured. That must be reported, not skipped.
	c, _ := New(Config{SegmentNanos: 1000 * ms, BoundarySearchNanos: 100 * ms})
	samples := []Sample{
		{Nanos: 0, Duration: 2000 * ms, Size: 10},
		{Nanos: 2000 * ms, Duration: 2000 * ms, Size: 10},
	}
	_, err := c.Cut(samples)
	if !mediaerr.Is(err, mediaerr.KindNoLegalBoundary) {
		t.Fatalf("err = %v, want no_legal_boundary", err)
	}
	if _, err := c.Cut(nil); !mediaerr.Is(err, mediaerr.KindNoLegalBoundary) {
		t.Fatalf("empty input err = %v, want no_legal_boundary", err)
	}
}

func TestCutMergeTail(t *testing.T) {
	c, _ := New(Config{SegmentNanos: 1000 * ms, BoundarySearchNanos: 100 * ms, MinSegmentNanos: 500 * ms})
	var samples []Sample
	tm := int64(0)
	for i := 0; i < 21; i++ {
		samples = append(samples, Sample{Nanos: tm, Duration: 100 * ms, Size: 100})
		tm += 100 * ms
	}
	man, err := c.Cut(samples)
	if err != nil {
		t.Fatalf("cut: %v", err)
	}
	last := man.Segments[len(man.Segments)-1]
	if last.Duration() < 500*ms {
		t.Fatalf("trailing segment of %d ns was not merged", last.Duration())
	}
}

func TestCutterConfigValidation(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("zero SegmentNanos accepted")
	}
	if _, err := New(Config{SegmentNanos: 1, BoundarySearchNanos: -1}); err == nil {
		t.Fatal("negative window accepted")
	}
}

func TestValidateManifestDetectsOverlap(t *testing.T) {
	man := Manifest{Segments: []Segment{
		{Start: 0, End: 100},
		{Start: 50, End: 150},
	}}
	if err := ValidateManifest(man); !mediaerr.Is(err, mediaerr.KindOverlap) {
		t.Fatalf("err = %v, want overlap", err)
	}
}
