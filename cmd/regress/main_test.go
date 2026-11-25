package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"rtpreplay/internal/fixture"
	"rtpreplay/report"
)

func writeTree(t *testing.T, mode string) string {
	t.Helper()
	root := t.TempDir()
	var err error
	switch mode {
	case "normal":
		err = fixture.GenerateNormal(root, []int{16, 32})
	case "damaged":
		err = fixture.GenerateDamaged(root, []int{16, 32})
	case "unrecoverable":
		err = fixture.GenerateUnrecoverable(root, []int{16, 32})
	}
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRunNormalExitZero(t *testing.T) {
	root := writeTree(t, "normal")
	code := run([]string{"--input", root, "--bitrates", "16,32", "--json"})
	if code != 0 {
		t.Fatalf("normal run exit = %d, want 0", code)
	}
}

func TestRunDamagedRecoverExitZero(t *testing.T) {
	root := writeTree(t, "damaged")
	code := run([]string{"--input", root, "--recover", "--json"})
	if code != 0 {
		t.Fatalf("recoverable damaged run exit = %d, want 0", code)
	}
}

func TestRunUnrecoverableExitTwo(t *testing.T) {
	root := writeTree(t, "unrecoverable")
	code := run([]string{"--input", root, "--bitrates", "16,32", "--json"})
	if code != 2 {
		t.Fatalf("unrecoverable run exit = %d, want 2", code)
	}
}

func TestRunMissingInputExitOne(t *testing.T) {
	if code := run([]string{"--bitrates", "16"}); code != 1 {
		t.Fatalf("missing input exit = %d, want 1", code)
	}
}

func TestRunBadBitrateExitOne(t *testing.T) {
	root := writeTree(t, "normal")
	if code := run([]string{"--input", root, "--bitrates", "0"}); code != 1 {
		t.Fatalf("bad bitrate exit = %d, want 1", code)
	}
	if code := run([]string{"--input", root, "--bitrates", "abc"}); code != 1 {
		t.Fatalf("non-numeric bitrate exit = %d, want 1", code)
	}
}

func TestRunSummaryCarriesRequiredFields(t *testing.T) {
	root := writeTree(t, "normal")
	opts, _ := parseFlags([]string{"--input", root, "--bitrates", "16,32", "--json"})
	summary, err := execute(opts)
	if err != nil {
		t.Fatal(err)
	}
	// Round-trip through JSON and assert every acceptance field key exists.
	raw, merr := json.Marshal(summary)
	if merr != nil {
		t.Fatal(merr)
	}
	var decoded map[string]json.RawMessage
	if uerr := json.Unmarshal(raw, &decoded); uerr != nil {
		t.Fatal(uerr)
	}
	for _, key := range []string{
		"sync_error_ms", "loss_recovery_rate", "latency_p95_ms",
		"segments", "silence_regions", "duplicates", "error_offset",
	} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("summary missing required field %s", key)
		}
	}
	// Acceptance thresholds on the healthy sample.
	if summary.SyncErrorMS > 20 {
		t.Fatalf("sync %f exceeds 20 ms", summary.SyncErrorMS)
	}
	if summary.LossRecoveryRate < 0.9 {
		t.Fatalf("recovery rate %f below 0.9", summary.LossRecoveryRate)
	}
	if summary.LatencyP95MS > 500 {
		t.Fatalf("p95 %f exceeds 500 ms", summary.LatencyP95MS)
	}
}

func TestRepairedTreeMatchesNormalSummary(t *testing.T) {
	// The damaged tree parses without unrecoverable loss, so repair applies.
	damagedRoot := writeTree(t, "damaged")
	dOpts, _ := parseFlags([]string{"--input", damagedRoot, "--bitrates", "16,32", "--json"})
	if _, err := execute(dOpts); err != nil {
		t.Fatalf("damaged tree must be recoverable, got %v", err)
	}

	repairedRoot := filepath.Join(t.TempDir(), "repaired")
	// Regenerate deterministic healthy tracks, mirroring cmd/fixture repair.
	if err := fixture.WriteRTPLog(
		filepath.Join(repairedRoot, "audio", "16.rtplog"),
		fixture.AudioSpecs(0, 0), fixture.AudioSSRC); err != nil {
		t.Fatal(err)
	}
	if err := fixture.WriteRTPLog(
		filepath.Join(repairedRoot, "video", "16.rtplog"),
		fixture.VideoSpecs(false), fixture.VideoSSRC); err != nil {
		t.Fatal(err)
	}
	// Healthy containers so metadata parsing contributes no error.
	if err := os.MkdirAll(fixture.MetaDir(repairedRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.MetaDir(repairedRoot), "sample.mp4"),
		fixture.HealthyMP4(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.MetaDir(repairedRoot), "sample.mxf"),
		fixture.HealthyMXF(), 0o644); err != nil {
		t.Fatal(err)
	}
	runOne := func(root string) *report.Summary {
		opts, _ := parseFlags([]string{"--input", root, "--bitrates", "16", "--json"})
		s, err := execute(opts)
		if err != nil {
			t.Fatalf("execute %s: %v", root, err)
		}
		return s
	}
	// Normal tree restricted to the same single tier that was repaired.
	normalRoot := t.TempDir()
	if err := fixture.GenerateNormal(normalRoot, []int{16}); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(runOne(normalRoot))
	b, _ := json.Marshal(runOne(repairedRoot))
	if string(a) != string(b) {
		t.Fatalf("repaired summary differs from normal\nnormal=%s\nrepaired=%s", a, b)
	}
}

func TestOutputPathOutsideInput(t *testing.T) {
	root := writeTree(t, "normal")
	out := filepath.Join(root, "report.json")
	opts, _ := parseFlags([]string{"--input", root, "--bitrates", "16", "--out", out})
	summary, err := execute(opts)
	if err != nil {
		t.Fatal(err)
	}
	// emit writes and must refuse the input-directory destination.
	emit(summary, opts)
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatal("report must not be written inside the input directory")
	}
}
