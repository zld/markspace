package cli

import (
	"os"
	"strings"
	"testing"
)

// captureStdout redirects os.Stdout while fn runs and returns what it wrote.
func captureStdout(t *testing.T, fn func() (int, error)) (string, int, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	code, ferr := fn()
	w.Close()
	os.Stdout = orig
	buf := make([]byte, 0, 64<<10)
	tmp := make([]byte, 4096)
	for {
		n, rerr := r.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if rerr != nil {
			break
		}
	}
	r.Close()
	return string(buf), code, ferr
}

func TestDemoRunsAndReportsCounters(t *testing.T) {
	dir := t.TempDir()
	out, code, err := captureStdout(t, func() (int, error) {
		return Demo([]string{"--seed", "17", "--workdir", dir, "--duration-ms", "4000", "--cameras", "2"})
	})
	if err != nil {
		t.Fatalf("Demo: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	for _, field := range []string{
		`"packet_total"`, `"lost"`, `"recovered"`, `"late_recomputed"`,
		`"max_av_skew_ms"`, `"sha256"`,
	} {
		if !strings.Contains(out, field) {
			t.Errorf("demo output missing %s", field)
		}
	}
}

func TestDemoRejectsBadFlag(t *testing.T) {
	if _, err := Demo([]string{"--no-such-flag"}); err == nil {
		t.Error("unknown flag must be rejected")
	}
}

func TestDemoRejectsPositional(t *testing.T) {
	if _, err := Demo([]string{"stray"}); err == nil {
		t.Error("positional argument must be rejected")
	}
}

func TestVerifyLocatesTruncation(t *testing.T) {
	dir := t.TempDir()
	out, code, err := captureStdout(t, func() (int, error) {
		return Verify([]string{"--fixture", "corrupt-truncated", "--expect-offset", "4096", "--workdir", dir})
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 for located damage", code)
	}
	if !strings.Contains(out, `"offset_deviation": 0`) {
		t.Errorf("damage not located within one byte:\n%s", out)
	}
	if !strings.Contains(out, `"error_count": 1`) {
		t.Errorf("expected a nonzero error count:\n%s", out)
	}
}

func TestVerifyCleanFixture(t *testing.T) {
	dir := t.TempDir()
	out, code, err := captureStdout(t, func() (int, error) {
		return Verify([]string{"--fixture", "generated-long", "--workdir", dir})
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if code != 0 {
		t.Fatalf("clean fixture exit code = %d, want 0", code)
	}
	if !strings.Contains(out, `"error_count": 0`) {
		t.Errorf("clean fixture reported errors:\n%s", out)
	}
}

func TestVerifyUnknownFixture(t *testing.T) {
	dir := t.TempDir()
	_, code, err := captureStdout(t, func() (int, error) {
		return Verify([]string{"--fixture", "no-such-thing", "--workdir", dir})
	})
	if err == nil || code != 1 {
		t.Fatalf("code=%d err=%v, want 1 and an error", code, err)
	}
}

func TestResumeMatchesReference(t *testing.T) {
	dir := t.TempDir()
	out, code, err := captureStdout(t, func() (int, error) {
		return Resume([]string{
			"--fixture", "generated-long", "--checkpoint-every", "1000",
			"--interrupt-after", "2500", "--workdir", dir,
		})
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out, `"resumed_from": 2000`) {
		t.Errorf("resume did not report checkpoint 2000:\n%s", out)
	}
	if !strings.Contains(out, `"resume_elapsed_ms"`) {
		t.Errorf("resume did not report latency:\n%s", out)
	}
	if !strings.Contains(out, `"ok": true`) {
		t.Errorf("resume digest mismatch:\n%s", out)
	}
}

func TestResumeRejectsBadInterval(t *testing.T) {
	_, code, err := captureStdout(t, func() (int, error) {
		return Resume([]string{"--checkpoint-every", "0"})
	})
	if err == nil || code != 1 {
		t.Fatalf("code=%d err=%v", code, err)
	}
}
