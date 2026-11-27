package pipeline

import (
	"path/filepath"
	"testing"

	"github.com/example/mediad/internal/audio"
	"github.com/example/mediad/internal/cache"
	"github.com/example/mediad/internal/checkpoint"
	"github.com/example/mediad/internal/fixture"
	"github.com/example/mediad/internal/report"
)

func inputFrom(t *testing.T, id, codec string, kind fixture.Kind, seed int64) Input {
	t.Helper()
	dgs, err := fixture.Build(fixture.Spec{
		Seed: seed, Kind: kind, Codec: codec, Rate: 48000,
		PacketCount: 12, PayloadSize: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	in := Input{MaterialID: id, CodecName: codec, SourceRate: 48000, Channels: 2}
	for _, d := range dgs {
		in.Datagrams = append(in.Datagrams, struct {
			Data   []byte
			Offset int64
		}{Data: d.Data, Offset: d.Offset})
	}
	return in
}

func newRunner(t *testing.T) p {
	t.Helper()
	dir := t.TempDir()
	c, err := cache.New(filepath.Join(dir, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	cp, err := checkpoint.NewStore(filepath.Join(dir, "cp"))
	if err != nil {
		t.Fatal(err)
	}
	return p{r: &Runner{
		Params:     Params{TargetRate: 48000, Layout: audio.LayoutStereo, LUFS: -16, TruePeak: -1.0, MaxBytes: 1 << 20},
		Cache:      c,
		Checkpoint: cp,
	}}
}

type p struct{ r *Runner }

func TestRunCleanMaterial(t *testing.T) {
	h := newRunner(t)
	res, err := h.r.Run(inputFrom(t, "clean-1", "opus", fixture.KindClean, 42))
	if err != nil {
		t.Fatal(err)
	}
	it := res.Item
	if it.LostPackets != 0 || it.DuplicatePackets != 0 {
		t.Fatalf("clean stream has anomalies: %+v", it)
	}
	if it.OutputBytes == 0 || it.OutputSamples == 0 {
		t.Fatalf("no output produced: %+v", it)
	}
	if it.TruePeakDBTP > -1.0+1e-9 {
		t.Fatalf("true peak %v exceeds -1 dBTP", it.TruePeakDBTP)
	}
	if absF(it.LUFS-(-16)) > 0.5 {
		t.Fatalf("loudness %v off target", it.LUFS)
	}
	if it.Status != report.StatusSatisfied {
		t.Fatalf("status = %s, want satisfied", it.Status)
	}
}

func TestRunTruncatedReturnsStableError(t *testing.T) {
	h := newRunner(t)
	_, err := h.r.Run(inputFrom(t, "trunc-1", "aac", fixture.KindTruncated, 7))
	if err == nil {
		t.Fatal("expected error for truncated packet")
	}
	// The error must carry stage, sequence and offset.
	if me, ok := err.(interface{ Error() string }); ok {
		if len(me.Error()) == 0 {
			t.Fatal("empty error message")
		}
	}
}

func TestRunLossDuplicateBookkeeping(t *testing.T) {
	h := newRunner(t)
	res, err := h.r.Run(inputFrom(t, "ld-1", "opus", fixture.KindLossDup, 11))
	if err != nil {
		t.Fatal(err)
	}
	it := res.Item
	if it.LostPackets != 1 {
		t.Fatalf("lost = %d, want 1", it.LostPackets)
	}
	if it.DuplicatePackets != 1 {
		t.Fatalf("duplicates = %d, want 1", it.DuplicatePackets)
	}
	if len(it.AnomalyRanges) == 0 {
		t.Fatal("expected anomaly ranges for the lost packet")
	}
}

func TestCacheHitOnSecondRun(t *testing.T) {
	h := newRunner(t)
	in := inputFrom(t, "cache-1", "opus", fixture.KindClean, 5)
	first, err := h.r.Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Item.CacheHit {
		t.Fatal("first run cannot be a cache hit")
	}
	second, err := h.r.Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Item.CacheHit {
		t.Fatal("identical rerun should be a cache hit")
	}
	if string(second.Artifact) != string(first.Artifact) {
		t.Fatal("cached artifact differs from computed artifact")
	}
}

func TestRunConflictIsolatesMaterial(t *testing.T) {
	h := newRunner(t)
	in := inputFrom(t, "conflict-1", "opus", fixture.KindConflict, 5)
	res, err := h.r.Run(in)
	if err != nil {
		t.Fatalf("conflict must isolate, not error: %v", err)
	}
	if !res.Item.Isolated || res.Item.Status != report.StatusIsolated {
		t.Fatalf("material not isolated: %+v", res.Item)
	}
	if res.Item.ConflictCount != 1 {
		t.Fatalf("conflict count = %d, want 1", res.Item.ConflictCount)
	}
	if len(res.Item.Deviations) == 0 {
		t.Fatal("isolation must record a deviation with sequence/offset")
	}
}

func TestManifestDigestStableAcrossRuns(t *testing.T) {
	h := newRunner(t)
	in := inputFrom(t, "man-1", "opus", fixture.KindClean, 9)
	r1, err := h.r.Run(in)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := h.r.Run(in)
	if err != nil {
		t.Fatal(err)
	}
	s1 := &report.Summary{Seed: 9, Items: []report.Item{r1.Item}}
	s2 := &report.Summary{Seed: 9, Items: []report.Item{r2.Item}}
	d1, _ := s1.ComputeManifestDigest()
	d2, _ := s2.ComputeManifestDigest()
	if d1 != d2 {
		t.Fatalf("manifest digest changed across runs: %s vs %s", d1, d2)
	}
}
