package segment

import (
	"errors"
	"testing"

	"mediadist/timeline"
)

// buildVideoTrack makes a 12 s 30 fps video on the common clock with keyframes
// at every 2 s. drift shifts every keyframe DTS by the given ticks to model
// encoder keyframe-position drift between bitrate tiers.
func buildVideoTrack(id uint32, keyframeDrift uint64) *timeline.Track {
	const frames = 360
	const frameDur = 3000 // 90000/30
	t := &timeline.Track{ID: id, Kind: timeline.KindVideo, Timescale: 90000,
		Color: &timeline.ColorDesc{Source: timeline.SourceContainer}}
	for i := 0; i < frames; i++ {
		dts := uint64(i) * frameDur
		key := i%60 == 0
		if key && i > 0 && keyframeDrift > 0 {
			dts += keyframeDrift
		}
		t.Samples = append(t.Samples, timeline.TrackSample{
			DTS: dts, CTS: dts, Dur: frameDur, Keyframe: key, Size: 1000,
		})
	}
	return t
}

func audioTrack() *timeline.Track {
	return &timeline.Track{ID: 2, Kind: timeline.KindAudio, Timescale: 48000,
		Layout: timeline.LayoutStereo, SampleRate: 48000,
		Samples: []timeline.TrackSample{{DTS: 0, CTS: 0, Dur: 1_080_000, Size: 500}}}
}

func testReps(driftA, driftB uint64) []Representation {
	return []Representation{
		{ID: "800k", TargetBitrateBps: 800_000, Video: buildVideoTrack(1, driftA), Audio: audioTrack()},
		{ID: "1600k", TargetBitrateBps: 1_600_000, Video: buildVideoTrack(3, driftB), Audio: audioTrack()},
	}
}

func TestAlignCommonBoundaries(t *testing.T) {
	cfg := DefaultConfig()
	plan, err := Align(testReps(0, 0), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// 12 s on a 2 s grid: boundaries at 0..12 => 7 boundaries, 6 segments.
	if len(plan.Boundaries) != 7 {
		t.Fatalf("got %d boundaries, want 7", len(plan.Boundaries))
	}
	interior := 0
	for i := 1; i < len(plan.Boundaries)-1; i++ {
		if plan.Boundaries[i].Switchable {
			interior++
		}
	}
	if interior < 5 {
		t.Fatalf("want at least 5 interior switchable boundaries, got %d", interior)
	}
	if plan.Boundaries[0].At != 0 || plan.Boundaries[len(plan.Boundaries)-1].At != plan.End {
		t.Fatal("boundary grid does not span [0, end]")
	}
}

func TestAlignKeyframeDriftMarksNonSwitchable(t *testing.T) {
	cfg := DefaultConfig()
	// Drift of 300 ms exceeds the 100 ms tolerance: boundary becomes
	// non-switchable but the grid time is unchanged (no audio movement).
	plan, err := Align(testReps(0, 27000), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(plan.Boundaries)-1; i++ {
		if plan.Boundaries[i].Switchable {
			t.Fatalf("boundary %d must be non-switchable under 300ms drift", i)
		}
	}
}

func TestAlignMissingKeyframePropagates(t *testing.T) {
	cfg := DefaultConfig()
	v := buildVideoTrack(1, 0)
	for i := range v.Samples {
		if i > 0 {
			v.Samples[i].Keyframe = false // only the first sync sample remains
		}
	}
	reps := []Representation{{ID: "only", TargetBitrateBps: 800_000, Video: v}}
	_, err := Align(reps, cfg)
	var le *timeline.LocError
	if !errors.As(err, &le) || le.Code != timeline.CodeMissingKeyframe ||
		le.Stage != timeline.StageSegment || le.TrackID != 1 {
		t.Fatalf("got %v, want KEYFRAME_MISSING at segment stage track 1", err)
	}
}

func TestBoundarySetConsistency(t *testing.T) {
	cfg := DefaultConfig()
	p1, err := Align(testReps(0, 0), cfg)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := Align(testReps(0, 0), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !BoundarySetEqual(p1, p2) {
		t.Fatal("identical inputs must yield identical boundary sets")
	}
}

func TestMeasureBitrateAndConformance(t *testing.T) {
	cfg := DefaultConfig()
	reps := testReps(0, 0)
	plan, err := Align(reps, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reps {
		rep, err := MeasureBitrate(r, plan, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Segments) != 6 {
			t.Fatalf("want 6 segment stats, got %d", len(rep.Segments))
		}
		if rep.Segments[0].ActualBitrateBps == 0 {
			t.Fatal("actual bitrate must be non-zero")
		}
	}
}

func TestManifestDurationConservationAndStableHash(t *testing.T) {
	cfg := DefaultConfig()
	reps := testReps(0, 0)
	plan, err := Align(reps, cfg)
	if err != nil {
		t.Fatal(err)
	}
	in := BuildInput{
		Reps: reps, Plan: plan, SSRC: 4242, PayloadType: 96,
		Reports:      map[string]*BitrateReport{},
		ColorSources: map[string]timeline.ColorSource{},
		Audio: map[string]AudioSummary{
			"800k":  {2, 2, 48000, 48000},
			"1600k": {2, 2, 48000, 48000},
		},
	}
	for _, r := range reps {
		rep, err := MeasureBitrate(r, plan, cfg)
		if err != nil {
			t.Fatal(err)
		}
		in.Reports[r.ID] = rep
		in.ColorSources[r.ID] = timeline.SourceContainer
	}
	m1, err := Build(in, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if m1.SegmentCount != 6 {
		t.Fatalf("segment_count=%d want 6", m1.SegmentCount)
	}
	if m1.AlignmentErrorTicks > cfg.MaxDurationErrorTicks {
		t.Fatalf("alignment error %d exceeds tolerance", m1.AlignmentErrorTicks)
	}
	if m1.ManifestDurationTicks != m1.TimelineEndTicks {
		t.Fatalf("duration not conserved: manifest=%d timeline=%d",
			m1.ManifestDurationTicks, m1.TimelineEndTicks)
	}
	if m1.SHA256 == "" || len(m1.SHA256) != 64 {
		t.Fatalf("manifest hash invalid: %q", m1.SHA256)
	}
	// Rebuild from identical inputs: every field including the hash must match.
	m2, err := Build(in, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if m2.SHA256 != m1.SHA256 {
		t.Fatalf("rerun hash differs:\n%s\n%s", m1.SHA256, m2.SHA256)
	}
}

func TestRTPSequenceAndTimestampWrap(t *testing.T) {
	pkg := NewRTPPackager(1234, 96)
	// A start time beyond 2^32 exercises timestamp wrap into uint32.
	_, seq, ts := pkg.packetFor(1<<32 + 90000)
	if ts != 90000 {
		t.Fatalf("timestamp wrap wrong: got %d want 90000", ts)
	}
	if seq != 0 {
		t.Fatalf("first sequence must be 0, got %d", seq)
	}
	pkg.seq = 65535
	_, seq2, _ := pkg.packetFor(0)
	if seq2 != 65535 {
		t.Fatalf("sequence at wrap wrong: %d", seq2)
	}
	_, seq3, _ := pkg.packetFor(0)
	if seq3 != 0 {
		t.Fatalf("sequence must wrap to 0, got %d", seq3)
	}
}

func TestPSNRDeterministicAndMonotone(t *testing.T) {
	low := EstimatePSNR(400_000).HundredthsDB
	mid := EstimatePSNR(1_600_000).HundredthsDB
	high := EstimatePSNR(3_000_000).HundredthsDB
	if !(low < mid && mid < high) {
		t.Fatalf("PSNR must rise with bitrate: %d %d %d", low, mid, high)
	}
	if EstimatePSNR(1_600_000).HundredthsDB != EstimatePSNR(1_600_000).HundredthsDB {
		t.Fatal("PSNR must be deterministic")
	}
}

func TestDurationErrorRejectedBeyondTolerance(t *testing.T) {
	cfg := DefaultConfig()
	plan := &Plan{End: 1_080_000, Boundaries: []Boundary{
		{At: 0}, {At: 540_000}, {At: 1_000_000}, // sums to 1_000_000 != end
	}}
	reps := testReps(0, 0)
	in := BuildInput{
		Reps: reps, Plan: plan,
		Reports:      map[string]*BitrateReport{},
		ColorSources: map[string]timeline.ColorSource{},
	}
	for _, r := range reps {
		rep, _ := MeasureBitrate(r, plan, cfg)
		in.Reports[r.ID] = rep
	}
	_, err := Build(in, cfg)
	var le *timeline.LocError
	if !errors.As(err, &le) || le.Code != timeline.CodeDurationConserv {
		t.Fatalf("got %v, want DURATION_NOT_CONSERVED", err)
	}
}
