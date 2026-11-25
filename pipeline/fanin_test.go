package pipeline

import (
	"math"
	"testing"

	"rtpreplay/ingest"
	"rtpreplay/media"
)

func repWithBoundaries(kind string, kbps int, rate uint32, times []float64) *Representation {
	st := &ingest.Stream{ClockRate: rate, BitrateKbps: kbps}
	r := &Representation{BitrateKbps: kbps, Kind: kind, Stream: st}
	for i, ms := range times {
		r.Boundaries = append(r.Boundaries, Boundary{TimeMS: ms, FirstRecord: i, ExtSeqStart: uint64(i)})
	}
	return r
}

func TestFanInAlignedAudioPasses(t *testing.T) {
	times := []float64{0, 200, 400, 600}
	reps := []*Representation{
		repWithBoundaries(KindAudio, 16, media.AudioClockRate, times),
		repWithBoundaries(KindAudio, 32, media.AudioClockRate, times),
		repWithBoundaries(KindAudio, 64, media.AudioClockRate, times),
	}
	res := FanIn(reps)
	if res.Failed || res.SyncErrorMS != 0 || res.CheckedPairs != 8 {
		t.Fatalf("aligned audio must pass: %+v", res)
	}
}

func TestFanInAudioOneSampleTolerance(t *testing.T) {
	oneSample := 1000.0 / 8000.0 // 0.125 ms
	base := []float64{0, 200, 400}
	within := []float64{0, 200 + oneSample/2, 400 + oneSample/2}
	beyond := []float64{0, 200 + oneSample*2, 400}

	ok := FanIn([]*Representation{
		repWithBoundaries(KindAudio, 16, media.AudioClockRate, base),
		repWithBoundaries(KindAudio, 32, media.AudioClockRate, within),
	})
	if ok.Failed {
		t.Fatalf("sub-sample delta must pass: %+v", ok.Mismatches)
	}

	bad := FanIn([]*Representation{
		repWithBoundaries(KindAudio, 16, media.AudioClockRate, base),
		repWithBoundaries(KindAudio, 32, media.AudioClockRate, beyond),
	})
	if !bad.Failed || len(bad.Mismatches) != 1 {
		t.Fatalf("over-tolerance delta must fail: %+v", bad)
	}
	m := bad.Mismatches[0]
	if m.ReferenceKbps != 16 || m.ComparedKbps != 32 || m.Index != 1 {
		t.Fatalf("mismatch attribution wrong: %+v", m)
	}
	if math.Abs(m.DeltaMS-oneSample*2) > 1e-9 || m.ToleranceMS != oneSample {
		t.Fatalf("delta/tolerance wrong: %+v", m)
	}
	if math.Abs(bad.SyncErrorMS-oneSample*2) > 1e-9 {
		t.Fatalf("sync error = %f", bad.SyncErrorMS)
	}
}

func TestFanInVideoOneFrameTolerance(t *testing.T) {
	// 30 fps-like spacing: median frame ~33.33 ms.
	base := []float64{0, 33.3333, 66.6667, 100}
	// A half-frame discrepancy stays inside one frame of tolerance.
	half := []float64{0, 33.3333 + 16, 66.6667, 100}
	// A two-frame discrepancy fails.
	two := []float64{0, 33.3333 + 70, 66.6667, 100}

	ok := FanIn([]*Representation{
		repWithBoundaries(KindVideo, 160, media.VideoClockRate, base),
		repWithBoundaries(KindVideo, 320, media.VideoClockRate, half),
	})
	if ok.Failed {
		t.Fatalf("within-frame delta must pass: %+v", ok.Mismatches)
	}

	bad := FanIn([]*Representation{
		repWithBoundaries(KindVideo, 160, media.VideoClockRate, base),
		repWithBoundaries(KindVideo, 320, media.VideoClockRate, two),
	})
	if !bad.Failed || len(bad.Mismatches) != 1 {
		t.Fatalf("two-frame delta must fail: %+v", bad)
	}
}

func TestFanInSingleRepresentationNoChecks(t *testing.T) {
	res := FanIn([]*Representation{
		repWithBoundaries(KindAudio, 16, media.AudioClockRate, []float64{0, 100}),
	})
	if res.Failed || res.CheckedPairs != 0 {
		t.Fatal("single representation has nothing to cross-check")
	}
}

func TestLatencyP95(t *testing.T) {
	// Perfectly paced stream: zero jitter.
	steady := mustStream(t, [][2]uint32{
		{0, 0}, {1, 160}, {2, 320}, {3, 480}, {4, 640},
	}, media.AudioClockRate, 16)
	reps, _ := Fanout([]*ingest.Stream{steady}, KindAudio, DefaultMaxGap)
	if got := LatencyP95MS(reps); got != 0 {
		t.Fatalf("steady stream p95 = %f, want 0", got)
	}

	// One late arrival: record 3 arrives 120 ms late. Its 140 ms interval
	// against the 20 ms sender cadence yields a single 120 ms jitter spike,
	// which the 95th percentile must surface.
	var jittered [][2]uint32
	for i := 0; i < 20; i++ {
		jittered = append(jittered, [2]uint32{uint32(i), uint32(i * 160)})
	}
	st := mustStreamWithArrivals(t, jittered, media.AudioClockRate, 16, map[int]int64{3: 120})
	reps2, _ := Fanout([]*ingest.Stream{st}, KindAudio, DefaultMaxGap)
	p95 := LatencyP95MS(reps2)
	if p95 != 120 {
		t.Fatalf("p95 = %f, want 120 ms spike", p95)
	}
}
