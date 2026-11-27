package report_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/zld/continuity/internal/clock"
	"github.com/zld/continuity/internal/index"
	"github.com/zld/continuity/internal/manifest"
	"github.com/zld/continuity/internal/report"
)

func TestBitrateDeviationWithinTolerance(t *testing.T) {
	s := report.Build(report.Inputs{
		TargetBitrate: 1_000_000,
		ActualBitrate: 1_010_000, // +1%
	})
	if math.Abs(s.BitrateDeviationPct-1.0) > 1e-9 {
		t.Fatalf("deviation = %f", s.BitrateDeviationPct)
	}
	if s.TargetBitrate != 1_000_000 || s.ActualBitrate != 1_010_000 {
		t.Fatal("bitrate fields not carried through")
	}
}

func TestZeroTargetDoesNotEmitNaN(t *testing.T) {
	s := report.Build(report.Inputs{TargetBitrate: 0, ActualBitrate: 100})
	if math.IsNaN(s.BitrateDeviationPct) || math.IsInf(s.BitrateDeviationPct, 0) {
		t.Fatalf("non-finite deviation: %f", s.BitrateDeviationPct)
	}
}

func TestBitrateFromBytesAndDuration(t *testing.T) {
	// 125000 bytes over 1 second = 1,000,000 bits/s.
	if got := report.Bitrate(125000, 1.0); got != 1_000_000 {
		t.Fatalf("bitrate = %d", got)
	}
	if got := report.Bitrate(125000, 0); got != 0 {
		t.Fatalf("zero duration must yield 0, got %d", got)
	}
}

func TestPSNRIdenticalAndDegraded(t *testing.T) {
	ref := []byte{10, 20, 30, 40, 200, 250}
	db, err := report.PSNR(ref, append([]byte{}, ref...), 255)
	if err != nil {
		t.Fatal(err)
	}
	if !math.IsInf(db, 1) {
		t.Fatalf("identical planes: want +Inf dB, got %f", db)
	}
	// Perturb each byte by at most 2 to force a finite score still above the
	// 35 dB acceptance floor, staying inside [0,255] to avoid byte wraparound.
	deg := make([]byte, len(ref))
	for i := range ref {
		switch {
		case ref[i] <= 2:
			deg[i] = ref[i] + 2
		case ref[i] >= 253:
			deg[i] = ref[i] - 2
		case i%2 == 0:
			deg[i] = ref[i] + 2
		default:
			deg[i] = ref[i] - 2
		}
	}
	db2, err := report.PSNR(ref, deg, 255)
	if err != nil {
		t.Fatal(err)
	}
	if db2 <= 35 || math.IsInf(db2, 1) {
		t.Fatalf("degraded PSNR = %f, want finite and >35", db2)
	}
	if _, err := report.PSNR([]byte{1}, []byte{1, 2}, 255); err == nil {
		t.Fatal("length mismatch must error")
	}
}

func TestSummaryJSONKeysAreStable(t *testing.T) {
	ix := index.New()
	ix.Add([]index.Entry{
		{Track: 1, Time: clock.MustMake(0, 25), Sync: true},
		{Track: 1, Time: clock.MustMake(1, 25), Sync: false},
	})
	s := report.Build(report.Inputs{
		TargetBitrate: 1000, ActualBitrate: 1000, PSNRDB: 42.5,
		RepairedTimestamps: 3, PeakQueueChunks: 2, CheckpointResumed: true,
		ResumedBlocks: 1, Index: ix,
		Manifest: manifest.Result{MaxErrorMS: 0.5, Counts: manifest.Counts{Backward: 1}},
	})
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"target_bitrate", "actual_bitrate", "bitrate_deviation_pct", "psnr_db",
		"manifest_max_error_ms", "repaired_timestamps", "index_entries",
		"checkpoint_resumed", "resumed_blocks", "peak_queue_chunks",
	} {
		if _, ok := m[key]; !ok {
			t.Fatalf("summary JSON missing contract key %q in %s", key, raw)
		}
	}
	if m["index_entries"].(float64) != 2 || m["sync_entries"].(float64) != 1 {
		t.Fatalf("index counts wrong: %s", raw)
	}
}
