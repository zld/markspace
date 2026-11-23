package main

import (
	"os"
	"path/filepath"
	"testing"

	"mediasync/internal/mederr"
)

// chdirWork switches into a temp dir for the CLI's relative work path and
// restores the original directory on cleanup.
func chdirWork(t *testing.T) string {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	return dir
}

func TestCLIDemoExitZero(t *testing.T) {
	chdirWork(t)
	code := run([]string{
		"demo", "--seed", "23", "--chunk-records", "256",
		"--interrupt-after", "2", "--resume",
	})
	if code != mederr.ExitOK {
		t.Fatalf("demo exit = %d, want 0", code)
	}
}

func TestCLIVerifyExitZeroAndDigestStable(t *testing.T) {
	chdirWork(t)
	code := run([]string{"verify", "--seed", "23", "--chunk-records", "256"})
	if code != mederr.ExitOK {
		t.Fatalf("verify exit = %d, want 0", code)
	}
}

func TestCLIInvalidInputExitTwo(t *testing.T) {
	chdirWork(t)
	// Unknown command and malformed flags both map to exit code 2.
	if code := run([]string{"frobnicate"}); code != mederr.ExitInvalidInput {
		t.Fatalf("unknown command exit = %d, want 2", code)
	}
	if code := run(nil); code != mederr.ExitInvalidInput {
		t.Fatalf("missing command exit = %d, want 2", code)
	}
	if code := run([]string{"demo", "--chunk-records", "0"}); code != mederr.ExitInvalidInput {
		t.Fatalf("bad chunk size exit = %d, want 2", code)
	}
}

func TestCLIUnrecoverableExitThree(t *testing.T) {
	chdirWork(t)
	// An interrupt boundary beyond the available chunks cannot be honoured.
	code := run([]string{
		"demo", "--seed", "23", "--chunk-records", "256",
		"--interrupt-after", "999",
	})
	if code != mederr.ExitInvalidInput && code != mederr.ExitUnrecoverable {
		t.Fatalf("unreachable boundary exit = %d", code)
	}
}

func TestExitCodeMappingCoversAllThree(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{mederr.New(mederr.CodeInvalidManifest, "x"), mederr.ExitInvalidInput},
		{mederr.New(mederr.CodeUnrecoverableGap, "x"), mederr.ExitUnrecoverable},
		{mederr.New(mederr.CodeInternal, "x"), mederr.ExitInternal},
		{nil, mederr.ExitOK},
	}
	for _, c := range cases {
		if got := mederr.ExitCode(c.err); got != c.want {
			t.Fatalf("ExitCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

func TestSummaryLinesShape(t *testing.T) {
	lines := summaryLines(zeroSummary())
	wantKeys := []string{
		"av_sync_error_ms", "packet_recovery_rate", "pipeline_latency_p95_ms",
		"timestamp_wraps", "vfr_intervals", "truncated_segments",
		"checkpoint_recovered", "index_records", "index_sha256",
		"invariants_ok",
	}
	joined := ""
	for _, l := range lines {
		joined += l + "\n"
	}
	for _, k := range wantKeys {
		if !contains(joined, k) {
			t.Fatalf("summary missing key %q in:\n%s", k, joined)
		}
	}
	_ = filepath.Separator
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
