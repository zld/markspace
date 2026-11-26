package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestDemoAndVerifyOutputs(t *testing.T) {
	fixtureRoot := t.TempDir()
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"demo", "--self-fixture", "--fixture-dir", fixtureRoot,
		"--event-ms", "1000", "--pre-ms", "500", "--post-ms", "500", "--out", out,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	text := stdout.String()
	for _, want := range []string{
		"frames=25", "pixels=6400", "audio_samples=48000", "peak_dbfs=-12.04",
		"video_align_error_ms=20", "audio_boundary_error_samples=0", "recovered=0 published=1 duplicate=0",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
	if hash := extractLine(text, "sha256="); len(hash) != 64 {
		t.Fatalf("hash length %d (%q)", len(hash), hash)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"verify", "--dir", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("verify code=%d err=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "status=verified") {
		t.Fatalf("verify output=%s", stdout.String())
	}
}

func TestTruncatedBoxExitTwoAndOffset(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"demo", "--self-fixture", "--case", "truncated-box",
		"--fixture-dir", t.TempDir(), "--out", t.TempDir(),
	}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d", code)
	}
	line := stderr.String()
	if !strings.Contains(line, "TRUNCATED_BOX offset=16") {
		t.Fatalf("stderr=%q", line)
	}
}

func TestRecoveryExitSeventyFiveThenCounters(t *testing.T) {
	fixtureRoot, out := t.TempDir(), t.TempDir()
	args := []string{"demo", "--self-fixture", "--case", "recover", "--fixture-dir", fixtureRoot, "--out", out}
	var stdout, stderr bytes.Buffer
	if code := Run(args, &stdout, &stderr); code != 75 {
		t.Fatalf("first code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	if code := Run(args, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "recovered=1 published=1 duplicate=0") {
		t.Fatalf("second code/output=%d %s", code, stdout.String())
	}
	secondHash := extractLine(stdout.String(), "sha256=")
	stdout.Reset()
	if code := Run(args, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "recovered=0 published=1 duplicate=0") {
		t.Fatalf("third code/output=%d %s", code, stdout.String())
	}
	thirdHash := extractLine(stdout.String(), "sha256=")
	if secondHash != thirdHash {
		t.Fatalf("hash changed %s != %s", secondHash, thirdHash)
	}
}

func TestHelpAndUnknownCaseExitCodes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"demo", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("demo help code=%d", code)
	}
	stdout.Reset()
	stderr.Reset()
	code := Run([]string{
		"demo", "--self-fixture", "--case", "bogus", "--fixture-dir", t.TempDir(), "--out", t.TempDir(),
	}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), `unknown --case "bogus"`) {
		t.Fatalf("unknown case code=%d stderr=%q", code, stderr.String())
	}
}

func extractLine(text, prefix string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}
