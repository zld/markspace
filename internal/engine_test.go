package continuity

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func demoRoot(t *testing.T, cfg DemoConfig) string {
	t.Helper()
	root := t.TempDir()
	if _, err := GenerateDemo(root, cfg); err != nil {
		t.Fatalf("generate demo: %v", err)
	}
	return root
}

func TestDemoAcceptance(t *testing.T) {
	root := demoRoot(t, DefaultDemoConfig())
	rep, err := Run(context.Background(), EngineConfig{
		Root: root, Format: []ManifestKind{ManifestHLS, ManifestDASH},
		LateWindow: lateWin(), QueueBytes: DefaultQueueBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Samples != 8 {
		t.Fatalf("samples=%d want 8", rep.Samples)
	}
	if rep.TimeDeltaTicks != 0 || rep.ByteDelta != 0 {
		t.Fatalf("deltas: time=%d byte=%d", rep.TimeDeltaTicks, rep.ByteDelta)
	}
	if rep.MappingMissing != 0 {
		t.Fatalf("mapping_missing=%d", rep.MappingMissing)
	}
	if rep.FailureLocation != "none" {
		t.Fatalf("failure_location=%q", rep.FailureLocation)
	}
	if len(rep.SHA256) != 64 {
		t.Fatalf("sha256=%q", rep.SHA256)
	}
	if len(rep.Lines) != 8 {
		t.Fatalf("per-sample lines=%d", len(rep.Lines))
	}
	if len(rep.MappingLines) != 2 || len(rep.Tracks) != 1 {
		t.Fatalf("mapping lines=%v tracks=%d", rep.MappingLines, len(rep.Tracks))
	}
	for _, line := range rep.Lines {
		if !strings.Contains(line, "bytes=[") || !strings.Contains(line, "time_delta=") {
			t.Fatalf("line lacks byte range/timing: %s", line)
		}
	}
	if rep.Segments == 0 || rep.Bytes == 0 {
		t.Fatalf("output media missing: segments=%d bytes=%d", rep.Segments, rep.Bytes)
	}
}

func TestDemoDeterministicAcrossRuns(t *testing.T) {
	run := func() string {
		root := demoRoot(t, DefaultDemoConfig())
		rep, err := Run(context.Background(), EngineConfig{
			Root: root, Format: []ManifestKind{ManifestHLS, ManifestDASH},
			LateWindow: lateWin(), QueueBytes: 1 << 20,
		})
		if err != nil {
			t.Fatal(err)
		}
		return rep.SHA256
	}
	a, b := run(), run()
	if a != b {
		t.Fatalf("digest differs across runs: %s vs %s", a, b)
	}
}

func TestDemoFormatSelectionConverges(t *testing.T) {
	root := demoRoot(t, DefaultDemoConfig())
	run := func(formats ...ManifestKind) string {
		rep, err := Run(context.Background(), EngineConfig{
			Root: root, Format: formats, LateWindow: lateWin(),
			QueueBytes: 1 << 20,
		})
		if err != nil {
			t.Fatal(err)
		}
		return rep.SHA256
	}
	hls := run(ManifestHLS)
	dash := run(ManifestDASH)
	both := run(ManifestHLS, ManifestDASH)
	if hls != dash || dash != both {
		t.Fatalf("format digests differ: hls=%s dash=%s both=%s", hls, dash, both)
	}
}

func TestDemoTruncatedBoxFault(t *testing.T) {
	cfg := DefaultDemoConfig()
	cfg.TruncateBackup = true
	root := demoRoot(t, cfg)
	_, err := Run(context.Background(), EngineConfig{
		Root: root, Format: []ManifestKind{ManifestHLS, ManifestDASH},
		LateWindow: lateWin(), QueueBytes: 1 << 20,
		Fault: FaultTruncatedBox,
	})
	if err == nil {
		t.Fatal("truncated fixture must fail")
	}
	if !IsKind(err, KindTruncated) {
		t.Fatalf("kind=%v", err)
	}
	loc := LocationOf(err)
	if loc.File != "backup/video/seg-00001.m4s" || loc.Offset <= 0 {
		t.Fatalf("loc=%+v", loc)
	}
	if ExitCode(err) != 2 {
		t.Fatalf("exit=%d", ExitCode(err))
	}
	if !strings.Contains(err.Error(), "backup/video/seg-00001.m4s@") {
		t.Fatalf("error lacks segment/offset: %v", err)
	}
}

func TestDemoBadReferenceFault(t *testing.T) {
	root := demoRoot(t, DefaultDemoConfig())
	// Corrupt main's playlist to reference a missing segment.
	hlsPath := filepath.Join(root, "main", "video", "index.m3u8")
	data, err := os.ReadFile(hlsPath)
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(data), "seg-00000.m4s", "seg-99999.m4s", 1)
	if err := os.WriteFile(hlsPath, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Run(context.Background(), EngineConfig{
		Root: root, Format: []ManifestKind{ManifestHLS},
		LateWindow: lateWin(), QueueBytes: 1 << 20,
	})
	if err == nil || !IsKind(err, KindBadReference) {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(FailureLocation(err), "seg-99999.m4s") {
		t.Fatalf("failure location=%q", FailureLocation(err))
	}
}

func TestDemoSwitchoverLateRecomputeAndReject(t *testing.T) {
	root := demoRoot(t, SwitchoverDemoConfig())
	work := t.TempDir()
	run := func() *Report {
		rep, err := Run(context.Background(), EngineConfig{
			Root: root, Format: []ManifestKind{ManifestHLS, ManifestDASH},
			LateWindow: lateWin(), QueueBytes: 1 << 20,
			Fault: FaultLateCheckpoint, WorkDir: work,
		})
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}
	first := run()
	if !first.Recovered {
		t.Fatal("run must report recovery")
	}
	if first.LateRecomputed != 1 {
		t.Fatalf("late_recomputed=%d want 1", first.LateRecomputed)
	}
	if first.LateRejected != 1 {
		t.Fatalf("late_rejected=%d want 1", first.LateRejected)
	}
	if first.TimeDeltaTicks != 0 || first.ByteDelta != 0 {
		t.Fatalf("deltas: %d/%d", first.TimeDeltaTicks, first.ByteDelta)
	}
	if first.Samples != 8 {
		t.Fatalf("samples=%d want 8", first.Samples)
	}
	if !strings.Contains(first.FailureLocation, "backup/") {
		t.Fatalf("failure_location=%q", first.FailureLocation)
	}
	second := run()
	if first.SHA256 != second.SHA256 {
		t.Fatalf("repeated recovery digest differs: %s vs %s", first.SHA256, second.SHA256)
	}
	if first.LateRecomputed != second.LateRecomputed || first.LateRejected != second.LateRejected {
		t.Fatalf("repeat counters differ: %+v vs %+v", first, second)
	}
	// The checkpoint is durable and reports its boundary.
	cp, err := LoadCheckpoint(filepath.Join(work, "checkpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.Samples) == 0 || cp.InputDigest == "" || cp.OutputDigest == "" {
		t.Fatalf("checkpoint incomplete: %+v", cp)
	}
}

func TestDemoCorruptedFixtureNeverSilentlySkipped(t *testing.T) {
	root := demoRoot(t, DefaultDemoConfig())
	// Delete a referenced segment: the run must fail, not skip it.
	if err := os.Remove(filepath.Join(root, "backup", "video", "seg-00003.m4s")); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), EngineConfig{
		Root: root, Format: []ManifestKind{ManifestHLS, ManifestDASH},
		LateWindow: lateWin(), QueueBytes: 1 << 20,
	})
	if err == nil {
		t.Fatal("missing segment must fail the run")
	}
	if ExitCode(err) != 2 {
		t.Fatalf("exit=%d", ExitCode(err))
	}
	if !strings.Contains(err.Error(), "seg-00003.m4s") {
		t.Fatalf("error must name the segment: %v", err)
	}
}

func TestDemoManifestDisagreementRejected(t *testing.T) {
	root := demoRoot(t, DefaultDemoConfig())
	// Make DASH claim a different duration for the second segment.
	mpdPath := filepath.Join(root, "main", "index.mpd")
	data, err := os.ReadFile(mpdPath)
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(data), `<S t="6000" d="6000"/>`, `<S t="6000" d="9000"/>`, 1)
	if broken == string(data) {
		t.Fatalf("fixture did not contain the expected timeline entry:\n%s", data)
	}
	if err := os.WriteFile(mpdPath, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Run(context.Background(), EngineConfig{
		Root: root, Format: []ManifestKind{ManifestHLS, ManifestDASH},
		LateWindow: lateWin(), QueueBytes: 1 << 20,
	})
	if err == nil || !IsKind(err, KindBadReference) {
		t.Fatalf("err=%v", err)
	}
}

func lateWin() Ticks {
	tk, err := FromMillis(500)
	if err != nil {
		panic(err)
	}
	return tk
}
