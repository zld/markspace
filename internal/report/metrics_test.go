package report

import (
	"errors"
	"testing"
)

func sampleOptions() Options {
	digest := [32]byte{1, 2, 3}
	return Options{
		Input: InputSummary{
			TimecodeScaleNS: 1_000_000,
			TrackBlocks:     map[uint32]int{1: 12, 2: 12, 3: 12},
			TotalBlocks:     36, InputBytes: 1670,
		},
		Output: OutputSummary{
			TracksMapped: 3, Samples: 36, OutputBytes: 1600,
			Clusters: 4, PayloadHash: digest, Published: true,
		},
		Recovery: RecoverySummary{
			Expected: 500, Received: 488, Duplicates: 27,
			Reordered: 44, Recovered: 44, Unrecoverable: 12,
		},
		SyncErrorsMS: []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		LatenciesMS:  []float64{2, 4, 6, 8, 10, 12, 14, 16, 18, 200},
		FirstDigest:  digest, SecondDigest: digest,
		DamagedCases: 5, Recoverable: 3, RejectedWrites: 2,
	}
}

func TestPercentileNearestRank(t *testing.T) {
	v := []float64{10, 20, 30, 40, 50}
	if got := Percentile(v, 95); got != 50 {
		t.Fatalf("p95 = %v want 50", got)
	}
	if got := Percentile(v, 50); got != 30 {
		t.Fatalf("p50 = %v want 30", got)
	}
	if got := Percentile(nil, 95); got != 0 {
		t.Fatalf("empty p95 = %v want 0", got)
	}
	// Input slice must not be mutated.
	orig := []float64{3, 1, 2}
	_ = Percentile(orig, 95)
	if orig[0] != 3 || orig[1] != 1 {
		t.Fatalf("Percentile mutated input: %v", orig)
	}
}

func TestBuildMetricsHealthy(t *testing.T) {
	m, err := Build(sampleOptions())
	if err != nil {
		t.Fatalf("unexpected invariant error: %v", err)
	}
	if m.SyncErrorMSP95 != 10 {
		t.Fatalf("sync p95 = %v", m.SyncErrorMSP95)
	}
	if m.PipelineLatencyMSP95 != 200 {
		t.Fatalf("latency p95 = %v", m.PipelineLatencyMSP95)
	}
	wantRate := round4(float64(500-12) / 500)
	if m.PacketRecoveryRate != wantRate || m.PacketRecoveryRate < 0.95 {
		t.Fatalf("recovery rate = %v want %v", m.PacketRecoveryRate, wantRate)
	}
	if m.TracksMapped != 3 || m.DamagedCases != 5 ||
		m.RecoverableCases != 3 || m.RejectedWrites != 2 {
		t.Fatalf("counters wrong: %+v", m)
	}
	if !m.RerunDigestEqual {
		t.Fatal("identical digests must report equal")
	}
}

func TestDigestInequalityDetected(t *testing.T) {
	o := sampleOptions()
	o.SecondDigest = [32]byte{9, 9, 9}
	m, err := Build(o)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if m.RerunDigestEqual {
		t.Fatal("different digests must not be reported equal")
	}
}

func TestSampleCountMismatchIsInvariant(t *testing.T) {
	o := sampleOptions()
	o.Output.Samples = 35 // one sample vanished
	if _, err := Build(o); !errors.Is(err, ErrInvariant) {
		t.Fatalf("want ErrInvariant, got %v", err)
	}
}

func TestTrackSumMismatchIsInvariant(t *testing.T) {
	o := sampleOptions()
	o.Input.TrackBlocks[1] = 10 // sum now 34 != total 36
	if _, err := Build(o); !errors.Is(err, ErrInvariant) {
		t.Fatalf("want ErrInvariant, got %v", err)
	}
}

func TestRecoveryRateBounds(t *testing.T) {
	if got := (RecoverySummary{Expected: 0}).RecoveryRate(); got != 1 {
		t.Fatalf("empty stream rate = %v want 1", got)
	}
	o := sampleOptions()
	o.Recovery.Expected = 10
	o.Recovery.Unrecoverable = 12 // impossible: more losses than span
	if _, err := Build(o); !errors.Is(err, ErrInvariant) {
		t.Fatalf("want ErrInvariant for impossible recovery, got %v", err)
	}
}

func TestTruncatedInputMarked(t *testing.T) {
	o := sampleOptions()
	o.Input.Truncated = true
	o.Input.TruncOffset = 900
	// A truncated input that still produced a full output is itself an
	// invariant failure; here we model a correctly suppressed write instead,
	// so no output exists and no digest comparison is possible.
	o.Output.Published = false
	o.Output.Samples = 30
	o.FirstDigest = [32]byte{}
	o.SecondDigest = [32]byte{}
	m, err := Build(o)
	if err != nil {
		t.Fatalf("suppressed partial output should be consistent: %v", err)
	}
	if m.RerunDigestEqual {
		t.Fatal("unpublished output cannot claim a digest equality")
	}
}

func TestDigestEqualityWithoutPublicationRejected(t *testing.T) {
	o := sampleOptions()
	o.Output.Published = false
	if _, err := Build(o); !errors.Is(err, ErrInvariant) {
		t.Fatalf("want ErrInvariant, got %v", err)
	}
}
