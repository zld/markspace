package fanout

import (
	"testing"

	"tsrepair/internal/mediaerr"
)

// feedHealthy drives a small anchored sequence: PCR first, then samples with
// matching 90 kHz PTS spacing.
func feedHealthy(t *testing.T, r *Representation) {
	t.Helper()
	// PCR at 1 second (27e6 ticks), local line in lockstep.
	r.ObservePCR(27_000_000, 27_000_000, 0)
	// Anchor PTS 0 binds to that PCR.
	r.AddSample(0, 0, true, 0)
	// One-second-spaced keyframes.
	r.AddSample(90_000, 90_000, true, 188)
	r.AddSample(180_000, 180_000, true, 376)
}

func TestIndependentRecoveryAndIndex(t *testing.T) {
	g := NewGroup()
	for id := 0; id < 3; id++ {
		g.Add(id, DefaultConfig(0x100+uint16(id)))
	}
	for _, r := range g.All() {
		feedHealthy(t, r)
	}
	succ := g.Successful()
	if len(succ) != 3 {
		t.Fatalf("successful reps=%d want 3", len(succ))
	}
	for id, r := range succ {
		idx := r.Index()
		if len(idx.Entries()) != 3 {
			t.Errorf("rep %d index entries=%d", id, len(idx.Entries()))
		}
		if r.AnchorMissing() {
			t.Errorf("rep %d reports missing anchor", id)
		}
	}
}

func TestFailedRepresentationExcluded(t *testing.T) {
	g := NewGroup()
	good := g.Add(0, DefaultConfig(0x100))
	bad := g.Add(1, DefaultConfig(0x101))
	feedHealthy(t, good)
	// Force a structural failure.
	bad.ec.Add(mediaerr.Fatal(mediaerr.CodeTimeOverflow, 0x101, 0, "simulated", "fail rep"))
	if !bad.Failed() {
		t.Fatal("expected failed representation")
	}
	if _, ok := g.Successful()[1]; ok {
		t.Fatal("failed representation must be excluded")
	}
	if _, ok := g.Successful()[0]; !ok {
		t.Fatal("healthy representation must remain")
	}
	if len(bad.Index().Entries()) != 0 {
		t.Fatal("failed representation yields no index")
	}
}

func TestMissingAnchorQueuesThenDrains(t *testing.T) {
	r := NewRepresentation(0, DefaultConfig(0x100))
	// Samples arrive before any PCR.
	r.AddSample(0, 0, true, 0)
	r.AddSample(90_000, 90_000, true, 188)
	if r.ec.CountByCode(mediaerr.CodeMissingPCRAnchor) != 1 {
		t.Fatalf("errors: %v", r.ec.All())
	}
	if len(r.Samples()) != 0 {
		t.Fatal("pre-anchor samples must be queued, not emitted")
	}
	// PCR arrives and drains the queue.
	r.ObservePCR(27_000_000, 27_000_000, 376)
	if len(r.Samples()) != 2 {
		t.Fatalf("drained samples=%d want 2", len(r.Samples()))
	}
	if r.Samples()[0].WallNanos != 1_000_000_000 || r.Samples()[1].WallNanos != 2_000_000_000 {
		t.Errorf("wall times wrong: %+v", r.Samples())
	}
}

func TestDriftCorrectionClamped(t *testing.T) {
	cfg := DefaultConfig(0x100)
	cfg.MaxDriftCorrection = 100
	r := NewRepresentation(0, cfg)
	r.ObservePCR(0, 0, 0)
	// A 5000-tick local jump must be clamped to the 100-tick single-step cap.
	r.ObservePCR(1000, 6000, 188)
	if r.ec.CountByCode(mediaerr.CodePCRDriftClamped) != 1 {
		t.Fatalf("errors: %v", r.ec.All())
	}
}

func TestSegmentsPartitioned(t *testing.T) {
	r := NewRepresentation(0, DefaultConfig(0x100))
	feedHealthy(t, r)
	g := NewGroup()
	g.reps[0] = r
	segs := g.SegmentsFor(2)
	if len(segs[0]) != 2 {
		t.Fatalf("segments=%d want 2", len(segs[0]))
	}
	if segs[0][0].SampleCount != 2 || segs[0][1].SampleCount != 1 {
		t.Errorf("partition sizes wrong: %+v", segs[0])
	}
	if segs[0][0].Representation != 0 {
		t.Error("representation id not propagated")
	}
}
