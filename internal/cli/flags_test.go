package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRejectsMissingWorkdir(t *testing.T) {
	if _, _, err := Parse([]string{"run", "--target-kbps", "300"}); err == nil {
		t.Fatal("missing workdir must error")
	}
}

func TestParseRejectsBadBitrateAndTiming(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"run", "--workdir", dir, "--target-kbps", "0"},
		{"run", "--workdir", dir, "--target-kbps", "-5"},
		{"run", "--workdir", dir, "--target-kbps", "300", "--segment-seconds", "0"},
		{"run", "--workdir", dir, "--target-kbps", "300", "--workers", "0"},
		{"fixture", "--workdir", dir, "--segments", "0", "--frames-per-segment", "96"},
		{"fixture", "--workdir", dir, "--segments", "3", "--frames-per-segment", "96", "--width", "177"},
	} {
		if _, _, err := Parse(args); err == nil {
			t.Errorf("expected error for %v", args)
		}
	}
}

func TestParseUnknownSubcommand(t *testing.T) {
	if _, _, err := Parse([]string{"explode", "--workdir", t.TempDir()}); err == nil ||
		!strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("expected unknown subcommand error, got %v", err)
	}
}

func TestInputPathCannotEscapeWorkdir(t *testing.T) {
	dir := t.TempDir()
	_, _, err := Parse([]string{"run", "--workdir", dir, "--target-kbps", "300", "--input", "../../etc/passwd"})
	if err == nil || !strings.Contains(err.Error(), "escapes the work directory") {
		t.Fatalf("expected path escape rejection, got %v", err)
	}
	_, _, err = Parse([]string{"run", "--workdir", dir, "--target-kbps", "300", "--input", "/etc/passwd"})
	if err == nil || !strings.Contains(err.Error(), "relative") {
		t.Fatalf("absolute input must be rejected, got %v", err)
	}
}

func TestManifestValidation(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "jobs.json")
	good := `{"jobs":[
		{"id":"a","input":"a.mp4","target_kbps":300},
		{"id":"b","input":"b.mp4","target_kbps":600}
	]}`
	if err := os.WriteFile(manifest, []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	sub, o, err := Parse([]string{"run", "--workdir", dir, "--manifest", "jobs.json"})
	if err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	if sub != "run" || len(o.Jobs) != 2 {
		t.Fatalf("manifest parse wrong: %+v", o)
	}

	dup := `{"jobs":[{"id":"x","input":"a.mp4","target_kbps":300},{"id":"x","input":"b.mp4","target_kbps":400}]}`
	os.WriteFile(manifest, []byte(dup), 0o644)
	if _, _, err := Parse([]string{"run", "--workdir", dir, "--manifest", "jobs.json"}); err == nil ||
		!strings.Contains(err.Error(), "duplicate task id") {
		t.Fatalf("duplicate id must be rejected, got %v", err)
	}

	escape := `{"jobs":[{"id":"x","input":"../escape.mp4","target_kbps":300}]}`
	os.WriteFile(manifest, []byte(escape), 0o644)
	if _, _, err := Parse([]string{"run", "--workdir", dir, "--manifest", "jobs.json"}); err == nil {
		t.Fatal("manifest input escaping workdir must be rejected")
	}
}

func TestManifestOutsideWorkdirRejected(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "jobs.json")
	os.WriteFile(outside, []byte(`{"jobs":[]}`), 0o644)
	if _, _, err := Parse([]string{"run", "--workdir", dir, "--manifest", outside}); err == nil {
		t.Fatal("manifest outside workdir must be rejected")
	}
}

func TestFixtureTailDefault(t *testing.T) {
	dir := t.TempDir()
	_, o, err := Parse([]string{"fixture", "--workdir", dir, "--segments", "5", "--frames-per-segment", "96"})
	if err != nil {
		t.Fatal(err)
	}
	if o.TailFrames != 24 {
		t.Errorf("default tail = %d, want quarter segment (24)", o.TailFrames)
	}
}

func TestExitCodeConstants(t *testing.T) {
	if ExitOK != 0 || ExitUsage != 2 || ExitRegression != 3 || ExitEncode != 4 || ExitInterrupted != 75 {
		t.Error("exit code contract changed")
	}
}
