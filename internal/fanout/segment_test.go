package fanout_test

import (
	"os"
	"testing"

	"mediapack/internal/fanout"
	"mediapack/internal/fixture"
	"mediapack/internal/ingest"
	"mediapack/internal/render"
	"mediapack/internal/timeline"
)

func writeFile(p string, b []byte) error { return os.WriteFile(p, b, 0o600) }

func setup(t *testing.T) (*timeline.Timeline, *render.Renderer) {
	t.Helper()
	s := fixture.Spec{
		Video: fixture.VideoSpec{
			TrackID: 1, Timescale: 24000, Width: 24, Height: 18,
			Colors: []fixture.ColorSpec{{Type: "nclx", Primaries: 1, Transfer: 1, Matrix: 1}},
			Delta:  1001, Frames: 12,
		},
	}
	dir := t.TempDir()
	p := dir + "/s.mp4"
	if err := writeFile(p, fixture.BuildMP4(s)); err != nil {
		t.Fatal(err)
	}
	c, err := ingest.ParseMP4(p)
	if err != nil {
		t.Fatal(err)
	}
	tl, err := timeline.Compose(c)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := render.SummaryDigest(c.Tracks)
	if err != nil {
		t.Fatal(err)
	}
	return tl, render.NewRenderer(digest)
}

func layers() []fanout.LayerSpec {
	return []fanout.LayerSpec{
		{Name: "low", BitrateKbps: 400, Width: 12, Height: 9, SSRC: 1000, FramesPerSeg: 4},
		{Name: "high", BitrateKbps: 1200, Width: 24, Height: 18, SSRC: 2000, FramesPerSeg: 4},
	}
}

func TestTwoLayersIndependentAndSharedClock(t *testing.T) {
	tl, r := setup(t)
	reps, err := fanout.Fanout(layers(), r, tl, fanout.SimConfig{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 2 {
		t.Fatalf("representations = %d, want 2", len(reps))
	}
	for _, rep := range reps {
		if len(rep.Segments) != 3 {
			t.Fatalf("%s segments = %d, want 3", rep.Layer.Name, len(rep.Segments))
		}
		// Shared clock: every segment's tick range matches the same frames.
		if rep.Segments[0].TickStart != 0 {
			t.Fatalf("%s first segment not at tick 0", rep.Layer.Name)
		}
	}
	// The two layers share the same frame ticks despite distinct geometry.
	if reps[0].Segments[1].TickStart != reps[1].Segments[1].TickStart {
		t.Fatal("layers must share the master clock")
	}
}

func TestDuplicatesDeduplicatedAndReorderedDetected(t *testing.T) {
	tl, r := setup(t)
	// High duplicate + reorder rates, zero loss: full completeness must hold.
	cfg := fanout.SimConfig{
		Seed: 7, LossRate: 0, DuplicateRate: 60,
		ReorderRate: 80, ReorderWindow: 3,
	}
	reps, err := fanout.Fanout(layers(), r, tl, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, rep := range reps {
		if rep.Duplicates == 0 {
			t.Fatalf("%s expected duplicates, got none", rep.Layer.Name)
		}
		if !rep.Reordered {
			t.Fatalf("%s reordering not detected", rep.Layer.Name)
		}
		if !rep.Complete() {
			t.Fatalf("%s must be complete without loss, gaps=%v", rep.Layer.Name, rep.Gaps)
		}
		// With zero loss every emitted sequence survives exactly once after
		// deduplication; the duplicates were extra, never replacing media.
		if rep.Received != rep.Sent {
			t.Fatalf("%s unique received=%d sent=%d", rep.Layer.Name, rep.Received, rep.Sent)
		}
		if rep.Received+rep.Duplicates <= rep.Sent {
			t.Fatalf("%s duplicate packets not accounted for: recv=%d dup=%d sent=%d",
				rep.Layer.Name, rep.Received, rep.Duplicates, rep.Sent)
		}
	}
}

func TestLossRecordedAsGapNoFabrication(t *testing.T) {
	tl, r := setup(t)
	cfg := fanout.SimConfig{Seed: 11, LossRate: 30, ReorderWindow: 1}
	reps, err := fanout.Fanout(layers(), r, tl, cfg)
	if err != nil {
		t.Fatal(err)
	}
	sawGap := false
	for _, rep := range reps {
		if len(rep.Gaps) == 0 {
			continue
		}
		sawGap = true
		// Every gap must be attached to a segment and report a positive range.
		for _, g := range rep.Gaps {
			if g.Count <= 0 || g.EndSeq < g.StartSeq {
				t.Fatalf("malformed gap: %+v", g)
			}
			if g.SegmentIndex < 0 || g.SegmentIndex >= len(rep.Segments) {
				t.Fatalf("gap segment index out of range: %+v", g)
			}
		}
		if rep.Complete() {
			t.Fatal("representation with gaps must not be complete")
		}
		// No fabricated media: received unique packets never exceed sent.
		if rep.Received > rep.Sent {
			t.Fatal("receiver fabricated packets")
		}
		// Independent processing: every segment still exists and is marked.
		if len(rep.Segments) != 3 {
			t.Fatalf("segments = %d, want 3", len(rep.Segments))
		}
	}
	if !sawGap {
		t.Fatal("30%% loss must produce at least one gap")
	}
}

func TestCheckpointAnchors(t *testing.T) {
	tl, r := setup(t)
	cfg := fanout.SimConfig{Seed: 3, LossRate: 0}
	reps, err := fanout.Fanout(layers(), r, tl, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, rep := range reps {
		if rep.LastCompletedSegment() != len(rep.Segments)-1 {
			t.Fatalf("%s last complete segment wrong", rep.Layer.Name)
		}
		last := rep.LastFrameIn(rep.LastCompletedSegment())
		if last != 11 {
			t.Fatalf("%s last frame = %d, want 11", rep.Layer.Name, last)
		}
	}
}

func TestFanoutDeterministic(t *testing.T) {
	tl, r := setup(t)
	cfg := fanout.DefaultSimConfig(17)
	first, err := fanout.Fanout(layers(), r, tl, cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fanout.Fanout(layers(), r, tl, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := range first {
		a, b := first[i], second[i]
		if a.Sent != b.Sent || a.Received != b.Received ||
			a.Duplicates != b.Duplicates || len(a.Gaps) != len(b.Gaps) {
			t.Fatalf("layer %s nondeterministic reassembly", a.Layer.Name)
		}
		for j := range a.Frames {
			if a.Frames[j].Hash != b.Frames[j].Hash || a.Frames[j].Tick != b.Frames[j].Tick {
				t.Fatal("frame hashes/ticks differ across reruns")
			}
		}
	}
}
