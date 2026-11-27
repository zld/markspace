package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// invoke runs the command in-process and captures output.
func invoke(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestSelftestCrashThenRecover(t *testing.T) {
	out := filepath.Join(t.TempDir(), "review")
	code, _, stderr := invoke(t, "-selftest", "-out", out, "-crash-after-artifact")
	if code != exitCrash {
		t.Fatalf("crash exit %d, want %d (stderr %q)", code, exitCrash, stderr)
	}
	if !strings.Contains(stderr, "phase=artifact_written committed=false") {
		t.Fatalf("crash output %q", stderr)
	}
	if _, err := os.Stat(filepath.Join(out, "manifest.json")); !os.IsNotExist(err) {
		t.Fatal("manifest exists after crash")
	}
	code, stdout, stderr := invoke(t, "-recover", "-out", out)
	if code != exitOK {
		t.Fatalf("recover exit %d (%q)", code, stderr)
	}
	if !strings.Contains(stdout, "recovered=1 published=1 missing=0 duplicate=0") {
		t.Fatalf("recover output %q", stdout)
	}
}

func TestSummaryContainsAcceptanceFields(t *testing.T) {
	out := filepath.Join(t.TempDir(), "review")
	code, stdout, stderr := invoke(t, "-selftest", "-out", out)
	if code != exitOK {
		t.Fatalf("exit %d (%q)", code, stderr)
	}
	for _, field := range []string{
		"items_total=", "items_ok=", "items_error=", "max_skew_ms=",
		"threshold_ms=40", "over_budget=", "anomaly_ranges=", "coverage=[", "frames=",
		"frame_sha256=", "rtp_duplicate=", "rtp_gap=", "mp4_invalid_samples=",
	} {
		if !strings.Contains(stdout, field) {
			t.Errorf("summary lacks %q", field)
		}
	}
	if strings.Count(stdout, "status=") != countField(t, stdout, "items_total=") {
		t.Errorf("per-item conclusions missing")
	}
}

// countField extracts "name=<int>" from the summary text.
func countField(t *testing.T, text, name string) int {
	t.Helper()
	re := regexp.MustCompile(regexp.QuoteMeta(name) + `(\d+)`)
	m := re.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("field %s absent", name)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCorruptInputsCountAsErrorsButKeepValidItems(t *testing.T) {
	out := filepath.Join(t.TempDir(), "review")
	code, stdout, _ := invoke(t, "-selftest", "-out", out)
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if countField(t, stdout, "items_error=") == 0 {
		t.Fatal("corrupt fixtures produced no errors")
	}
	if countField(t, stdout, "items_ok=") == 0 {
		t.Fatal("valid items were dropped alongside corrupt ones")
	}
	// The broken SRT's survivor and the damaged clip's rejected sample are
	// both accounted for.
	if !strings.Contains(stdout, "broken.srt:17") {
		t.Error("valid cue after corrupt cues missing")
	}
	if !strings.Contains(stdout, "clip-truncated.mp4:track1") {
		t.Error("damaged MP4 track not reported")
	}
	if countField(t, stdout, "mp4_invalid_samples=") != 1 {
		t.Errorf("mp4_invalid_samples = %d, want 1", countField(t, stdout, "mp4_invalid_samples="))
	}
	if countField(t, stdout, "rtp_duplicate=") != 1 {
		t.Errorf("rtp_duplicate = %d, want 1", countField(t, stdout, "rtp_duplicate="))
	}
	if countField(t, stdout, "rtp_gap=") != 1 {
		t.Errorf("rtp_gap = %d, want 1", countField(t, stdout, "rtp_gap="))
	}
}

func TestOverBudgetCountMatchesAnomalyList(t *testing.T) {
	out := filepath.Join(t.TempDir(), "review")
	_, stdout, _ := invoke(t, "-selftest", "-out", out)
	groups := 0
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "anomaly groups=") {
			groups += len(strings.Split(strings.Fields(strings.Split(line, " groups=")[1])[0], ","))
		}
	}
	listed := strings.Count(stdout, "anomaly groups=")
	if groups != countField(t, stdout, "over_budget=") {
		t.Fatalf("over_budget=%d but %d groups listed across %d ranges",
			countField(t, stdout, "over_budget="), groups, listed)
	}
	if listed != countField(t, stdout, "anomaly_ranges=") {
		t.Fatalf("anomaly_ranges=%d but %d ranges printed", countField(t, stdout, "anomaly_ranges="), listed)
	}
	if countField(t, stdout, "max_skew_ms=") <= 40 {
		t.Fatal("expected a skew over the 40 ms budget")
	}
}

func TestRerunKeepsFrameHashAndArtifactCount(t *testing.T) {
	out := filepath.Join(t.TempDir(), "review")
	code, first, _ := invoke(t, "-selftest", "-out", out)
	if code != exitOK {
		t.Fatalf("first exit %d", code)
	}
	before, err := os.ReadDir(filepath.Join(out, "artifacts", "review"))
	if err != nil {
		t.Fatal(err)
	}
	code, second, stderr := invoke(t, "-selftest", "-out", out)
	if code != exitOK {
		t.Fatalf("rerun exit %d (%q)", code, stderr)
	}
	if !strings.Contains(second, "frame_sha256_match=true") {
		t.Fatalf("rerun did not confirm the frame hash: %q", second)
	}
	after, err := os.ReadDir(filepath.Join(out, "artifacts", "review"))
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("artifact count changed %d -> %d", len(before), len(after))
	}
	h1 := digestLine(t, first)
	h2 := digestLine(t, second)
	if h1 == "" || h1 != h2 {
		t.Fatalf("frame digest changed: %q vs %q", h1, h2)
	}
}

// digestLine extracts the frame_sha256 value from a summary line such as
// "frames=5 frame_sha256=abc".
func digestLine(t *testing.T, text string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if i := strings.Index(line, "frame_sha256="); i >= 0 {
			return strings.TrimSpace(line[i:])
		}
	}
	return ""
}

func TestVerifyDetectsTampering(t *testing.T) {
	out := filepath.Join(t.TempDir(), "review")
	if code, _, _ := invoke(t, "-selftest", "-out", out); code != exitOK {
		t.Fatal("selftest failed")
	}
	if code, _, _ := invoke(t, "-verify", "-out", out); code != exitOK {
		t.Fatal("clean verify failed")
	}
	target := filepath.Join(out, "artifacts", "review", "frame-000.png")
	if err := os.WriteFile(target, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := invoke(t, "-verify", "-out", out)
	if code != exitInput {
		t.Fatalf("tampered verify exit %d", code)
	}
	if !strings.Contains(stdout, "corrupt=1") {
		t.Fatalf("verify output %q", stdout)
	}
}

func TestRollbackThenRepublish(t *testing.T) {
	out := filepath.Join(t.TempDir(), "review")
	if code, _, _ := invoke(t, "-selftest", "-out", out); code != exitOK {
		t.Fatal("selftest failed")
	}
	code, stdout, _ := invoke(t, "-rollback", "-out", out)
	if code != exitOK || !strings.Contains(stdout, "rolled_back=6") {
		t.Fatalf("rollback %d %q", code, stdout)
	}
	if _, err := os.Stat(filepath.Join(out, "artifacts", "review")); !os.IsNotExist(err) {
		t.Fatal("artifacts survived rollback")
	}
	code, _, stderr := invoke(t, "-selftest", "-out", out)
	if code != exitOK {
		t.Fatalf("republish after rollback failed: %d %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(out, "manifest.json")); err != nil {
		t.Fatal("manifest missing after republish")
	}
}

func TestInputErrorsDoNotFailExitStatus(t *testing.T) {
	// Deliberately corrupt fixtures are part of the self-test contract: they
	// are counted and reported, and the process still completes.
	out := filepath.Join(t.TempDir(), "review")
	code, stdout, _ := invoke(t, "-selftest", "-out", out)
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stdout, "input_errors=") {
		t.Fatal("input errors not reported")
	}
}

func TestAllInputsStayInsideOut(t *testing.T) {
	out := filepath.Join(t.TempDir(), "review")
	if code, _, _ := invoke(t, "-selftest", "-out", out); code != exitOK {
		t.Fatal("selftest failed")
	}
	err := filepath.Walk(out, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(out, path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(rel, "..") {
			t.Errorf("file escaped output root: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestArgumentValidation(t *testing.T) {
	if code, _, _ := invoke(t); code != exitInput {
		t.Fatal("missing -out accepted")
	}
	if code, _, _ := invoke(t, "-out", t.TempDir(), "-crash-after", "nope"); code != exitInput {
		t.Fatal("bad crash point accepted")
	}
	if code, _, _ := invoke(t, "-out", t.TempDir(), "-frames", "0"); code != exitInput {
		t.Fatal("bad frame count accepted")
	}
	if code, _, _ := invoke(t, "-out", ".", "-selftest"); code != exitInput {
		t.Fatal("dangerous output root accepted")
	}
}

func TestRateValidation(t *testing.T) {
	out := filepath.Join(t.TempDir(), "review")
	if code, _, _ := invoke(t, "-selftest", "-out", out, "-rate", "30"); code != exitInput {
		t.Fatal("unsupported rate accepted")
	}
	for _, rate := range []string{"24", "25", "29.97", "50"} {
		o := filepath.Join(t.TempDir(), "r"+strings.ReplaceAll(rate, ".", "_"))
		if code, _, stderr := invoke(t, "-selftest", "-out", o, "-rate", rate); code != exitOK {
			t.Errorf("rate %s failed: %d %q", rate, code, stderr)
		}
	}
}
