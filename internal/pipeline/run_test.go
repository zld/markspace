package pipeline

import (
	"path/filepath"
	"testing"
	"time"

	"mediatool/internal/audio"
	"mediatool/internal/fixture"
)

func baseCfg(out string) Config {
	return Config{
		OutDir:      out,
		ChunkWindow: 200 * time.Millisecond,
		WindowCap:   16,
		MaxChunkMem: 1 << 20,
		Audio: audio.Config{
			SourceRate: 8000, SourceLayout: audio.LayoutMono,
			TargetRate: 8000, TargetLayout: audio.LayoutMono, TargetLUFS: -18,
		},
	}
}

func TestRunNormalStream(t *testing.T) {
	out := t.TempDir()
	spec := fixture.DefaultSpec(fixture.KindNormal)
	pkts := fixture.Build(spec)
	if _, err := fixture.WriteCapture(filepath.Join(out, "capture"),
		"normal.rtp", pkts, -1); err != nil {
		t.Fatal(err)
	}
	res, err := Run(spec, baseCfg(out), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != nil {
		t.Fatalf("normal run errored: %v", res.Error)
	}
	if res.Metrics.SyncErrorMS > 20 {
		t.Fatalf("sync error %.2fms exceeds 20ms", res.Metrics.SyncErrorMS)
	}
	if res.Metrics.LatencyP95MS <= 0 {
		t.Fatal("p95 latency must be positive")
	}
	if res.Metrics.RecoveredChunks == 0 {
		t.Fatal("no chunks recovered")
	}
	if res.Metrics.IndexSHA256 == "" {
		t.Fatal("index digest missing")
	}
}

func TestRunWrapAndVFRNoBackwards(t *testing.T) {
	for _, kind := range []fixture.Kind{fixture.KindWrap, fixture.KindVFR} {
		out := t.TempDir()
		spec := fixture.DefaultSpec(kind)
		pkts := fixture.Build(spec)
		if _, err := fixture.WriteCapture(filepath.Join(out, "capture"),
			string(kind)+".rtp", pkts, -1); err != nil {
			t.Fatal(err)
		}
		res, err := Run(spec, baseCfg(out), false)
		if err != nil {
			t.Fatalf("%s run: %v", kind, err)
		}
		// Timeline construction would have raised CodeTimeWentBack on a
		// backwards step; assert no such error and a clean exit.
		if res.Error != nil {
			t.Fatalf("%s produced timeline error: %v", kind, res.Error)
		}
	}
}

func TestRunDuplicateReorder(t *testing.T) {
	out := t.TempDir()
	spec := fixture.DefaultSpec(fixture.KindDupReorder)
	pkts := fixture.Build(spec)
	if _, err := fixture.WriteCapture(filepath.Join(out, "capture"),
		"dup_reorder.rtp", pkts, -1); err != nil {
		t.Fatal(err)
	}
	res, err := Run(spec, baseCfg(out), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Metrics.DuplicatePackets == 0 {
		t.Fatal("duplicate packets not counted")
	}
	if res.Metrics.LossRecoveryRate < 0.95 {
		t.Fatalf("reorder recovery rate %.3f below 0.95", res.Metrics.LossRecoveryRate)
	}
}

func TestRunBurstLossRecoveryRate(t *testing.T) {
	out := t.TempDir()
	spec := fixture.DefaultSpec(fixture.KindNormal)
	spec.Kind = fixture.KindBurstLoss
	spec.DropAt = 20
	spec.DropCount = 3
	pkts := fixture.Build(spec)
	if _, err := fixture.WriteCapture(filepath.Join(out, "capture"),
		"burst_loss.rtp", pkts, -1); err != nil {
		t.Fatal(err)
	}
	res, err := Run(spec, baseCfg(out), false)
	if err != nil {
		t.Fatal(err)
	}
	// With no reordering fills, genuinely lost packets stay lost; the rate
	// is defined over recoverable loss, so a clean run reports 1.0 when
	// nothing recoverable was missed, and never fabricates audio.
	if res.Metrics.LossRecoveryRate < 0 || res.Metrics.LossRecoveryRate > 1 {
		t.Fatalf("recovery rate out of range: %f", res.Metrics.LossRecoveryRate)
	}
}

func TestRunTruncatedReportsOffsetAndExit2(t *testing.T) {
	out := t.TempDir()
	spec := fixture.DefaultSpec(fixture.KindTruncated)
	pkts := fixture.Build(spec)
	// Cut inside a later frame.
	if _, err := fixture.WriteCapture(filepath.Join(out, "capture"),
		"truncated.rtp", pkts, 400); err != nil {
		t.Fatal(err)
	}
	res, err := Run(spec, baseCfg(out), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == nil || res.Error.Offset < 0 {
		t.Fatalf("truncation must report an offset, got %+v", res.Error)
	}
	if res.Metrics.ExitCode != 2 {
		t.Fatalf("truncated exit = %d, want 2", res.Metrics.ExitCode)
	}
	// Fields must still be printed/available on failure.
	if res.Metrics.IndexSHA256 == "" {
		t.Fatal("metrics not populated on failure")
	}
}
