package pipeline

import (
	"os"
	"os/exec"
	"testing"

	"mediad/internal/model"
	"mediad/internal/recover"
	"mediad/internal/verify"
)

func TestSegmentInWindow(t *testing.T) {
	segs := []recover.Segment{
		{ID: 0, StartNanos: 0, EndNanos: 1_000_000_000},
		{ID: 1, StartNanos: 1_000_000_000, EndNanos: 2_000_000_000},
		{ID: 2, StartNanos: 2_000_000_000, EndNanos: 3_000_000_000},
	}
	// Window [1.5s, 2.5s]: no fully-contained segment; intersecting is seg 1.
	got := segmentInWindow(segs, 1_500_000_000, 2_500_000_000)
	if got.ID != 1 {
		t.Fatalf("intersecting pick = %d, want 1", got.ID)
	}
	// Window [1s,3s]: seg 1 and 2 fully contained; prefer earliest = 1.
	got = segmentInWindow(segs, 1_000_000_000, 3_000_000_000)
	if got.ID != 1 {
		t.Fatalf("contained pick = %d, want 1", got.ID)
	}
	// Window before everything: fall back to first segment.
	got = segmentInWindow(segs, 10_000_000_000, 11_000_000_000)
	if got.ID != 0 {
		t.Fatalf("fallback pick = %d, want 0", got.ID)
	}
}

func TestPrimaryAndFindTrack(t *testing.T) {
	n := normalizedForTest()
	if id := primaryVideoTrack(n); id != 1 {
		t.Fatalf("primary video track = %d, want 1", id)
	}
	if tr := findTrack(n, 99); tr != nil {
		t.Fatal("missing track must return nil")
	}
	if tr := findTrack(n, 2); tr == nil || tr.Kind != "subtitle" {
		t.Fatalf("findTrack failed: %+v", tr)
	}
}

func TestTimingErrorZeroForExactConversion(t *testing.T) {
	n := normalizedForTest()
	if got := timingError(n); got != 0 {
		t.Fatalf("exact rational conversion must yield 0 error, got %v", got)
	}
}

func ffmpegOnPath() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// TestDemoEndToEnd runs the full four-scenario pipeline against locally
// generated material and asserts the documented acceptance behaviour.
func TestDemoEndToEnd(t *testing.T) {
	if !ffmpegOnPath() {
		t.Skip("ffmpeg not on PATH; skipping end-to-end demo")
	}
	work := t.TempDir()
	cfg := DemoConfig{
		WorkDir: work, Scenarios: []string{"normal", "damage", "replace", "metadata"},
		FromNanos: 1_000_000_000, ToNanos: 5_000_000_000,
		TargetKbps: 400, DurationSec: 6, Runner: verify.FFmpegRunner{},
	}
	rows, err := Demo(cfg)
	if err != nil {
		t.Fatalf("demo: %v", err)
	}
	byName := map[string]*Row{}
	for _, r := range rows {
		byName[r.Scenario] = r
	}

	normal := byName["normal"]
	if normal == nil {
		t.Fatal("normal row missing")
	}
	if fails := verify.AcceptanceFailures(verify.Metrics{
		TargetKbps: normal.TargetKbps, ActualKbps: normal.ActualKbps,
		BitrateDeltaPct: normal.BitrateDeltaPct, PSNRDB: normal.PSNRDB,
		MaxTimeErrorMs: normal.ManifestMaxErrorMs,
	}); len(fails) != 0 {
		t.Fatalf("normal scenario failed acceptance: %v", fails)
	}

	damage := byName["damage"]
	if damage == nil || damage.AnomalyCount != 1 || damage.RecoveredSegments != 1 {
		t.Fatalf("damage must report one recovered anomaly: %+v", damage)
	}

	replace := byName["replace"]
	if replace == nil || !replace.IndexRebuilt {
		t.Fatalf("replace must rebuild the index: %+v", replace)
	}

	meta := byName["metadata"]
	if meta == nil || !meta.IndexRebuilt || !meta.CacheHit {
		t.Fatalf("metadata must rebuild index but reuse cache: %+v", meta)
	}

	// Every row is a recoverable/healthy outcome -> exit 0.
	for _, r := range rows {
		if r.ExitCode != model.ExitOK {
			t.Fatalf("row %s exit = %d", r.Scenario, r.ExitCode)
		}
	}
	// A manifest file was retained.
	if _, err := os.Stat(work + "/manifests/latest.json"); err != nil {
		t.Fatalf("manifest not retained: %v", err)
	}
}

// TestDemoSourceCorruptIsUnrecoverable verifies the exit-code-2 path: a source
// that cannot be recomputed yields an unrecoverable row while the manifest is
// still written. This exercises the contract at the Go layer (the built binary
// returns process exit code 2; note that `go run` normalizes it to 1).
func TestDemoSourceCorruptIsUnrecoverable(t *testing.T) {
	if !ffmpegOnPath() {
		t.Skip("ffmpeg not on PATH; skipping end-to-end demo")
	}
	work := t.TempDir()
	cfg := DemoConfig{
		WorkDir: work, Scenarios: []string{"corrupt"},
		FromNanos: 1_000_000_000, ToNanos: 5_000_000_000,
		TargetKbps: 400, DurationSec: 6, Runner: verify.FFmpegRunner{},
	}
	rows, err := Demo(cfg)
	if err != nil {
		t.Fatalf("demo: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want one row, got %d", len(rows))
	}
	r := rows[0]
	if r.ExitCode != model.ExitUnrecoverable {
		t.Fatalf("corrupt row exit = %d, want %d", r.ExitCode, model.ExitUnrecoverable)
	}
	if r.UnrecoverableSegments != 1 || r.RecoveredSegments != 0 {
		t.Fatalf("expected one unrecoverable, zero recovered: %+v", r)
	}
	if r.AnomalyCount != 1 {
		t.Fatalf("unrecoverable range must still be in the manifest: %+v", r)
	}
	if _, err := os.Stat(work + "/manifests/latest.json"); err != nil {
		t.Fatalf("manifest must be retained for an unrecoverable input: %v", err)
	}
}
