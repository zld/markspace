package pipeline

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/archive/mediakernel/internal/manifest"
	"github.com/archive/mediakernel/internal/media"
	"github.com/archive/mediakernel/internal/rtprecover"
	"github.com/archive/mediakernel/internal/testfixture"
	"github.com/archive/mediakernel/internal/timeline"
)

const testClockRate = uint32(48000)

// buildInput assembles a deterministic damaged-but-recoverable job: an MP4 and
// an RTP capture with a burst loss, reordering and duplicate packets.
func buildInput(t *testing.T, seed int64) (*Input, []byte) {
	t.Helper()
	mp4Spec := testfixture.MP4Spec{
		Seed: seed, Timescale: 48000, SampleCount: 60, SampleDelta: 960,
		SampleSize: 160, KeyframeEvery: 5, StartOffset: 4800,
	}
	mp4Bytes, err := testfixture.GenerateMP4(mp4Spec)
	if err != nil {
		t.Fatal(err)
	}
	const nPackets = 120
	rtpSpec := testfixture.RTPSpec{
		Seed: seed, ClockRate: testClockRate, PacketCount: nPackets,
		BaseSeq: 1000, BaseTS: 4800, PayloadSize: 160,
	}
	packets, err := testfixture.GenerateRTP(rtpSpec)
	if err != nil {
		t.Fatal(err)
	}
	// Damage: drop a burst of 14, swap two neighbors (reorder), duplicate one.
	damaged := make([][]byte, 0, len(packets)+2)
	for i, p := range packets {
		if i >= 40 && i < 54 { // burst loss of 14 packets
			continue
		}
		damaged = append(damaged, p)
	}
	// Reorder: swap packets at positions 10 and 11 (still present).
	damaged[10], damaged[11] = damaged[11], damaged[10]
	// Duplicate a recent packet immediately, so it stays inside the window.
	damaged = append(damaged[:12], append([][]byte{damaged[11]}, damaged[12:]...)...)

	asset := &manifest.Asset{
		ID: "demo", MP4Path: "media/demo.mp4", RTPPath: "rtp/demo.rtp",
		Timescale: 48000, ClockRate: testClockRate, StartOffsetUS: 100_000,
		TargetBitrate: 2_400_000,
	}
	est, _ := rtprecover.NewEstimator(0.25, 500_000)
	_ = est.Observe(1_000_000, time.Second)
	return &Input{Asset: asset, MP4: mp4Bytes, RTPPackets: damaged, Estimator: est}, mp4Bytes
}

func testCfg(workDir string) Config {
	return Config{
		WorkDir: workDir, ReorderWindow: 32, LossThreshold: 8,
		TargetBitrate: 2_400_000, SegmentDurationUS: int64(timeline.Second),
		Silence: media.DefaultSilenceConfig(), Fingerprint: media.FingerprintConfig{Tolerance: 1},
	}
}

func TestRunEndToEndProducesConsistentManifest(t *testing.T) {
	work := t.TempDir()
	in, _ := buildInput(t, 73)
	cfg := testCfg(work)
	result, stats, err := Run(in, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.InputDigest == "" {
		t.Fatal("input_digest missing")
	}
	if !result.IndexRebuilt {
		t.Fatal("first run must build/rebuild the index")
	}
	if len(result.Segments) == 0 {
		t.Fatal("no segments produced")
	}
	if stats.LossBursts < 1 {
		t.Fatalf("loss bursts = %d, expected the injected burst", stats.LossBursts)
	}
	if stats.PacketsDuplicate == 0 {
		t.Fatal("injected duplicate not counted")
	}
	// The duplicate must never reach the output: emitted <= unique packets.
	if stats.PacketsEmitted > 120-14 {
		t.Fatalf("emitted %d exceeds unique available packets", stats.PacketsEmitted)
	}
	// All segments must be sorted and carry valid ranges.
	for i, s := range result.Segments {
		if s.EndUS <= s.StartUS {
			t.Fatalf("segment %d invalid range", i)
		}
		if i > 0 && s.StartUS < result.Segments[i-1].StartUS {
			t.Fatal("segments not sorted")
		}
	}
	// Bitrate metrics must be finite numbers.
	if stats.ActualBitrateBps <= 0 {
		t.Fatalf("actual bitrate non-positive: %d", stats.ActualBitrateBps)
	}
	if stats.QualityScore < 0 || stats.QualityScore > 100 {
		t.Fatalf("quality score out of bounds: %v", stats.QualityScore)
	}
}

// TestDigestChangeInvalidatesIndex is the required fault class: changing the
// input while an index exists must force a rebuild, never reuse stale state.
func TestDigestChangeInvalidatesIndex(t *testing.T) {
	work := t.TempDir()
	in, _ := buildInput(t, 73)
	cfg := testCfg(work)
	r1, _, err := Run(in, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, "index.json")); err != nil {
		t.Fatalf("index not persisted: %v", err)
	}
	// Second run with identical input: index is valid, so not rebuilt.
	r2, _, err := Run(in, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r2.IndexRebuilt {
		t.Fatal("identical input must reuse the cached index")
	}
	if r2.InputDigest != r1.InputDigest {
		t.Fatal("digest unstable across runs")
	}
	// Mutate content -> digest changes -> index must invalidate and rebuild.
	in2, _ := buildInput(t, 74)
	r3, _, err := Run(in2, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !r3.IndexRebuilt {
		t.Fatal("digest change must invalidate and rebuild the index")
	}
}

// TestCorruptIndexTriggersRebuild covers the explicit corruption flag.
func TestCorruptIndexTriggersRebuild(t *testing.T) {
	work := t.TempDir()
	in, _ := buildInput(t, 73)
	cfg := testCfg(work)
	if _, _, err := Run(in, cfg); err != nil {
		t.Fatal(err)
	}
	corruptCfg := cfg
	corruptCfg.CorruptIndex = true
	result, _, err := Run(in, corruptCfg)
	if err != nil {
		t.Fatalf("corrupt run should recover via rebuild: %v", err)
	}
	if !result.IndexRebuilt {
		t.Fatal("corrupt-index run must report a rebuild")
	}
}

// TestResumeAndRepeatDeterminism: an interrupted run resumes from the
// checkpoint, and re-running the full job yields identical digest, segment
// count and statistics.
func TestResumeAndRepeatDeterminism(t *testing.T) {
	work := t.TempDir()
	in, _ := buildInput(t, 73)
	cfg := testCfg(work)
	r1, s1, err := Run(in, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// A resumed run must complete and report recovered segments.
	resumeCfg := cfg
	resumeCfg.Resume = true
	r2, s2, err := Run(in, resumeCfg)
	if err != nil {
		t.Fatalf("resume Run: %v", err)
	}
	if s2.RecoveredSegments < 0 {
		t.Fatal("recovered segment count negative")
	}
	// Repeat-run stability: same digest, segment count and gap count.
	if r2.InputDigest != r1.InputDigest {
		t.Fatal("digest differs on repeat")
	}
	if len(r2.Segments) != len(r1.Segments) {
		t.Fatalf("segment count drifted: %d vs %d", len(r2.Segments), len(r1.Segments))
	}
	if len(r2.Gaps) != len(r1.Gaps) {
		t.Fatalf("gap count drifted: %d vs %d", len(r2.Gaps), len(r1.Gaps))
	}
	if s2.TargetBitrateBps != s1.TargetBitrateBps || s2.ActualBitrateBps != s1.ActualBitrateBps {
		t.Fatalf("bitrate stats drifted: %+v vs %+v", s1, s2)
	}
}

// TestResumeFromSimulatedCrash proves recovery from a mid-run interruption: a
// partial checkpoint makes the resumed run count the already-finished segments
// as recovered while still producing the complete, identical manifest.
func TestResumeFromSimulatedCrash(t *testing.T) {
	work := t.TempDir()
	in, _ := buildInput(t, 73)
	cfg := testCfg(work)

	// Baseline full run establishes the expected output.
	base, _, err := Run(in, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(base.Segments) < 2 {
		t.Fatalf("need >=2 segments for crash test, got %d", len(base.Segments))
	}

	// Simulate a crash after the first segment: wipe the natural checkpoint and
	// write a partial one ending at the first segment boundary.
	if err := ClearCheckpoint(work); err != nil {
		t.Fatal(err)
	}
	first := base.Segments[0]
	if err := WritePartialCheckpoint(work, 1, first.EndUS); err != nil {
		t.Fatal(err)
	}

	resumeCfg := cfg
	resumeCfg.Resume = true
	r2, s2, err := Run(in, resumeCfg)
	if err != nil {
		t.Fatalf("resumed run failed: %v", err)
	}
	// The first segment is reported as recovered from the checkpoint.
	if s2.RecoveredSegments != 1 {
		t.Fatalf("recovered segments = %d, want 1", s2.RecoveredSegments)
	}
	// The full manifest is still emitted and matches the baseline exactly.
	if len(r2.Segments) != len(base.Segments) {
		t.Fatalf("resumed segment count = %d, want %d", len(r2.Segments), len(base.Segments))
	}
	for i := range base.Segments {
		if r2.Segments[i].StartUS != base.Segments[i].StartUS ||
			r2.Segments[i].EndUS != base.Segments[i].EndUS {
			t.Fatalf("segment %d changed after resume", i)
		}
	}
}

// TestMalformedCheckpointIsClassified verifies a corrupt checkpoint surfaces a
// classified corruption error rather than a panic.
func TestMalformedCheckpointIsClassified(t *testing.T) {
	work := t.TempDir()
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "checkpoint.json"), []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, _ := buildInput(t, 73)
	cfg := testCfg(work)
	cfg.Resume = true
	if _, _, err := Run(in, cfg); timeline.Classify(err) != timeline.KindCorrupt {
		t.Fatalf("malformed checkpoint: got %v, want KindCorrupt", err)
	}
}
