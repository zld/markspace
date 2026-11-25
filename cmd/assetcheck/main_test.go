package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"assetcheck/internal/fixture"
)

type cliEnv struct {
	fixDir   string
	indexDir string
}

func newCLIEnv(t *testing.T) *cliEnv {
	t.Helper()
	dir := t.TempDir()
	if _, err := fixture.Generate(filepath.Join(dir, "fix")); err != nil {
		t.Fatal(err)
	}
	return &cliEnv{fixDir: filepath.Join(dir, "fix"), indexDir: filepath.Join(dir, "idx")}
}

func (e *cliEnv) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func digestOf(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "summary-digest:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "summary-digest:"))
		}
	}
	return ""
}

func TestCLIGoodExitZeroAndStableDigest(t *testing.T) {
	e := newCLIEnv(t)
	args := func(in string) []string {
		return []string{"-input", filepath.Join(e.fixDir, in), "-index", e.indexDir,
			"-threshold", "sample-rate=48000,channels=6,max-gap-frames=1024,max-corrupt-ratio=0.001"}
	}
	code, out, _ := e.run(t, args("good.wav")...)
	if code != 0 {
		t.Fatalf("good exit=%d", code)
	}
	if !strings.Contains(out, "corrupt-ratio=0.000000") || !strings.Contains(out, "[PASS] max-corrupt-ratio") {
		t.Fatal("good report missing clean ratio/PASS")
	}
	d1 := digestOf(out)
	code, out2, _ := e.run(t, args("good.wav")...)
	if code != 0 {
		t.Fatalf("good second exit=%d", code)
	}
	if digestOf(out2) != d1 {
		t.Fatal("repeated run digest differs")
	}
}

func TestCLITruncatedExitTwoAndRange(t *testing.T) {
	e := newCLIEnv(t)
	code, out, _ := e.run(t, "-input", filepath.Join(e.fixDir, "truncated.wav"),
		"-index", e.indexDir, "-threshold", "max-corrupt-ratio=0.001")
	if code != 2 {
		t.Fatalf("truncated exit=%d out-tail=%s", code, out[len(out)-400:])
	}
	if !strings.Contains(out, "start-frame=95232") || !strings.Contains(out, "end-frame=96000") {
		t.Fatal("truncated report missing located range")
	}
	if !strings.Contains(out, "E1012") || !strings.Contains(out, "E1014") {
		t.Fatal("truncated report missing offset error codes")
	}
}

func TestCLIBadHeaderExitThree(t *testing.T) {
	e := newCLIEnv(t)
	code, _, errb := e.run(t, "-input", filepath.Join(e.fixDir, "badheader.wav"), "-index", e.indexDir)
	if code != 3 {
		t.Fatalf("badheader exit=%d", code)
	}
	if !strings.Contains(errb, "E1003") {
		t.Fatalf("want E1003 in stderr: %s", errb)
	}
}

func TestCLIMissingInputExitThree(t *testing.T) {
	e := newCLIEnv(t)
	code, _, _ := e.run(t, "-input", filepath.Join(e.fixDir, "missing.wav"), "-index", e.indexDir)
	if code != 3 {
		t.Fatalf("missing exit=%d, want 3", code)
	}
}

func TestCLIInjectAndResumeMatchesClean(t *testing.T) {
	e := newCLIEnv(t)
	good := filepath.Join(e.fixDir, "good.wav")

	code, clean, _ := e.run(t, "-input", good, "-index", e.indexDir)
	if code != 0 {
		t.Fatalf("clean exit=%d", code)
	}
	want := digestOf(clean)

	code, inj, errb := e.run(t, "-input", good, "-index", e.indexDir,
		"-inject-failure", "after-index-prepare")
	if code != 4 {
		t.Fatalf("inject exit=%d err=%s", code, errb)
	}
	if !strings.Contains(inj, "injected-failure=after-index-prepare") {
		t.Fatal("inject report missing marker")
	}

	code, resume, _ := e.run(t, "-input", good, "-index", e.indexDir, "-resume")
	if code != 0 {
		t.Fatalf("resume exit=%d", code)
	}
	if !strings.Contains(resume, "rolled-back=[tx-") {
		t.Fatal("resume report missing rollback")
	}
	if got := digestOf(resume); got != want {
		t.Fatalf("resume digest %s != clean %s", got, want)
	}
}

func TestCLIMutateContentInvalidatesParseCache(t *testing.T) {
	e := newCLIEnv(t)
	code, out, _ := e.run(t, "-input", filepath.Join(e.fixDir, "replaced.wav"),
		"-index", e.indexDir, "-mutate", "content")
	if code != 0 {
		t.Fatalf("mutate content exit=%d", code)
	}
	if !strings.Contains(out, "parse-cache: INVALIDATED") ||
		!strings.Contains(out, "content-fingerprint-mismatch") {
		t.Fatal("missing content cache invalidation")
	}
}

func TestCLIScanBudgetReportsLastFrame(t *testing.T) {
	e := newCLIEnv(t)
	code, out, _ := e.run(t, "-input", filepath.Join(e.fixDir, "good.wav"),
		"-index", e.indexDir, "-max-scan-frames", "1000")
	if code != 0 {
		t.Fatalf("budget-limited run exit=%d", code)
	}
	if !strings.Contains(out, "scan-budget-exceeded: processed through last-valid-frame=999") {
		t.Fatalf("missing budget last-frame report:\n%s", out)
	}
	if !strings.Contains(out, "E2005") {
		t.Fatal("budget finding must carry E2005")
	}
}

func TestCLIJSONReport(t *testing.T) {
	e := newCLIEnv(t)
	code, out, _ := e.run(t, "-input", filepath.Join(e.fixDir, "good.wav"),
		"-index", e.indexDir, "-report", "json")
	if code != 0 {
		t.Fatalf("json exit=%d", code)
	}
	if !strings.Contains(out, `"summary-digest": "sha256:`) || !strings.Contains(out, `"eval-pass": true`) {
		t.Fatal("JSON report missing expected fields")
	}
}
