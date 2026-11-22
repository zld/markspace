package demo

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/medialab/medialab/internal/clock"
)

func baseCfg(t *testing.T, fault Fault) Config {
	t.Helper()
	return Config{
		Seed: 42, DurationSec: 60, Tracks: 3, LossBurst: 7,
		WindowCapacity: 64, QueueCapacity: 8, TargetBps: 2_500_000,
		OutDir: t.TempDir(), Fault: fault,
	}
}

func TestNormalRunMeetsInvariants(t *testing.T) {
	cfg := baseCfg(t, FaultNone)
	res := Run(context.Background(), cfg)
	if res.ExitCode != ExitOK || res.FailedStage != "" {
		t.Fatalf("normal run failed: stage=%s err=%v", res.FailedStage, res.Err)
	}
	s := res.Summary
	if s.Frames < 100 || s.Frames > 5000 {
		t.Fatalf("frames = %d outside expected range", s.Frames)
	}
	if s.PeakQueue > cfg.QueueCapacity {
		t.Fatalf("peak queue %d exceeds capacity %d", s.PeakQueue, cfg.QueueCapacity)
	}
	if s.AlignmentErrorMs > 1 {
		t.Fatalf("alignment %dms exceeds 1ms budget", s.AlignmentErrorMs)
	}
	if s.Gaps != 0 || s.LateDropped != 0 || s.ChecksumBad != 0 {
		t.Fatalf("healthy run reports faults: %+v", s)
	}
	if s.Duplicates != 1 {
		t.Fatalf("duplicates = %d, want deterministic 1", s.Duplicates)
	}
	if s.FrameSHA256 == "" {
		t.Fatal("missing folded frame digest")
	}
}

func TestDeterminismAcrossRuns(t *testing.T) {
	cfg1 := baseCfg(t, FaultNone)
	cfg2 := baseCfg(t, FaultNone)
	r1 := Run(context.Background(), cfg1)
	r2 := Run(context.Background(), cfg2)
	if r1.Summary.FrameSHA256 != r2.Summary.FrameSHA256 {
		t.Fatal("frame digest differs across identical runs")
	}
	if r1.Summary.Frames != r2.Summary.Frames ||
		r1.Summary.QualityMean != r2.Summary.QualityMean {
		t.Fatal("timeline output differs across identical runs")
	}
}

func TestCompositeCorruptFlowsThroughStages(t *testing.T) {
	cfg := baseCfg(t, FaultComposite)
	res := Run(context.Background(), cfg)
	if res.ExitCode != ExitProcessErr {
		t.Fatalf("exit = %d, want 1", res.ExitCode)
	}
	if res.FailedStage != stageRender {
		t.Fatalf("failed stage = %q, want render", res.FailedStage)
	}
	if res.Summary.Gaps <= 0 {
		t.Fatal("corrupt run must report at least one gap")
	}
	if res.Summary.Frames == 0 {
		t.Fatal("recovered frame count must be reported despite render failure")
	}
	if res.Summary.ChecksumBad != 1 {
		t.Fatalf("checksum faults = %d, want 1", res.Summary.ChecksumBad)
	}
}

func TestTruncatedManifestIsInputError(t *testing.T) {
	cfg := baseCfg(t, FaultTruncated)
	res := Run(context.Background(), cfg)
	if res.ExitCode != ExitInputErr || res.FailedStage != stageManifest {
		t.Fatalf("truncated: exit=%d stage=%s", res.ExitCode, res.FailedStage)
	}
	me, ok := clock.As(res.Err)
	if !ok || me.Kind != clock.KindTruncated {
		t.Fatalf("want truncated media error, got %v", res.Err)
	}
}

func TestEachFaultScenario(t *testing.T) {
	cases := []struct {
		fault      Fault
		wantStage  string
		wantExit   int
		needsFault bool
	}{
		{FaultChecksum, "", ExitOK, true},
		{FaultLate, "", ExitOK, true},
		{FaultBurstLoss, "", ExitOK, true},
		{FaultBandwidth, "", ExitOK, false},
		{FaultRender, stageRender, ExitProcessErr, false},
	}
	for _, tc := range cases {
		cfg := baseCfg(t, tc.fault)
		res := Run(context.Background(), cfg)
		if res.ExitCode != tc.wantExit {
			t.Fatalf("%s: exit=%d want %d", tc.fault, res.ExitCode, tc.wantExit)
		}
		if res.FailedStage != tc.wantStage {
			t.Fatalf("%s: stage=%q want %q", tc.fault, res.FailedStage, tc.wantStage)
		}
		if tc.needsFault && res.Summary.Gaps == 0 &&
			res.Summary.LateDropped == 0 && res.Summary.ChecksumBad == 0 {
			t.Fatalf("%s: no fault counters recorded", tc.fault)
		}
	}
}

func TestBandwidthCollapseDegrades(t *testing.T) {
	cfg := baseCfg(t, FaultBandwidth)
	res := Run(context.Background(), cfg)
	if res.Summary.TargetBps >= cfg.TargetBps {
		t.Fatalf("target did not degrade under collapse: %d", res.Summary.TargetBps)
	}
}

func TestEvidenceMP4RoundTrip(t *testing.T) {
	payload := []byte(`{"manifest":"evidence","tick_hz":1000}`)
	ok, err := EvidenceRoundTrip(payload)
	if err != nil || !ok {
		t.Fatalf("MP4 evidence round trip failed: ok=%v err=%v", ok, err)
	}
	var buf evidenceBuffer
	if err := WriteEvidenceMP4(&buf, payload); err != nil {
		t.Fatal(err)
	}
	if len(buf.Bytes()) < 16 {
		t.Fatal("MP4 output implausibly small")
	}
}

func TestArtifactsOnDisk(t *testing.T) {
	cfg := baseCfg(t, FaultNone)
	res := Run(context.Background(), cfg)
	if res.ExitCode != ExitOK {
		t.Fatal(res.Err)
	}
	for _, name := range []string{"manifest.json", "evidence.mp4", "summary.json"} {
		if _, err := os.Stat(filepath.Join(cfg.OutDir, name)); err != nil {
			t.Fatalf("artifact %s missing: %v", name, err)
		}
	}
}

func TestInvalidConfigRejected(t *testing.T) {
	cfg := baseCfg(t, FaultNone)
	cfg.QueueCapacity = 0
	res := Run(context.Background(), cfg)
	if res.ExitCode != ExitInputErr {
		t.Fatalf("invalid config exit = %d, want 2", res.ExitCode)
	}
}
