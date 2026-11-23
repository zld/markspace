package pipeline

import (
	"math"
	"path/filepath"
	"testing"

	"mediaroute/internal/journal"
)

// demoOptions is the acceptance scenario: three tracks, one loss burst on the
// H.264 track, an eight-packet reorder window.
func demoOptions(t *testing.T) Options {
	t.Helper()
	return Options{
		Seed:          41,
		LossBurst:     3,
		ReorderWindow: 8,
		JournalDir:    filepath.Join(t.TempDir(), "journal"),
	}
}

func TestRunProducesAcceptanceMetrics(t *testing.T) {
	opts := demoOptions(t)
	r, err := NewRunner(opts)
	if err != nil {
		t.Fatal(err)
	}
	m, err := r.Run()
	if err != nil {
		t.Fatal(err)
	}
	if got := m.RoundtripRate; got < 0.99 || got > 0.99 {
		t.Fatalf("roundtrip_rate = %v want 0.99", got)
	}
	// Exactly one source keyframe is cut by the loss burst, so every other
	// complete keyframe must be indexed and validate. The printed ratio is
	// rounded to two decimals by the CLI.
	if m.KeyframeHits != m.KeyframeTotal-1 {
		t.Fatalf("keyframe hits = %d/%d, want all but the burst-truncated one",
			m.KeyframeHits, m.KeyframeTotal)
	}
	if got := math.Round(m.KeyframeHitRate*100) / 100; got != 0.95 {
		t.Fatalf("keyframe_hit_rate = %v want 0.95", m.KeyframeHitRate)
	}
	if m.Segments != SegmentCount || m.SegmentMatches != SegmentCount {
		t.Fatalf("segments=%d matched=%d want %d/%d",
			m.Segments, m.SegmentMatches, SegmentCount, SegmentCount)
	}
	if m.Duplicates < 1 || m.Reordered < 1 {
		t.Fatalf("dedup/reorder evidence missing: %+v", m)
	}
	if m.Lost != opts.LossBurst {
		t.Fatalf("lost_burst = %d want %d", m.Lost, opts.LossBurst)
	}
	if m.CongestionPeak < 1 {
		t.Fatalf("bandwidth model did not engage: peak=%d", m.CongestionPeak)
	}
	// Conservation: accepted + lost == transmitted distinct packets.
	if m.Accepted+m.Lost != m.TruthPackets {
		t.Fatalf("packet conservation broken: %d+%d != %d",
			m.Accepted, m.Lost, m.TruthPackets)
	}
	// Every exported frame must come from a committed segment. Reading the
	// WAL back is independent of the in-memory bookkeeping the runner keeps.
	recs, _, err := journal.ReadWAL(opts.JournalDir)
	if err != nil {
		t.Fatal(err)
	}
	committed := map[string]bool{}
	for _, rec := range recs {
		if rec.Kind == journal.RecCommit {
			committed[rec.Key] = true
		}
	}
	for _, fr := range r.Manifest().Segments {
		if !committed[fr.SegmentID] {
			t.Fatalf("exported segment %s was never committed", fr.SegmentID)
		}
	}
}

func TestRunIsDeterministic(t *testing.T) {
	opts := demoOptions(t)
	h1 := runHash(t, opts)
	// Same directory again must reproduce the same manifest hash.
	h2 := runHash(t, opts)
	// A fresh directory must too.
	opts.JournalDir = filepath.Join(t.TempDir(), "journal")
	h3 := runHash(t, opts)
	if h1 != h2 || h2 != h3 {
		t.Fatalf("manifest hash unstable: %s %s %s", h1, h2, h3)
	}
}

func runHash(t *testing.T, opts Options) string {
	t.Helper()
	_ = Cleanup(opts.JournalDir)
	return runHashNoCleanup(t, opts)
}

func runHashNoCleanup(t *testing.T, opts Options) string {
	t.Helper()
	r, err := NewRunner(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(); err != nil {
		t.Fatal(err)
	}
	h, err := r.Manifest().ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	return h
}
