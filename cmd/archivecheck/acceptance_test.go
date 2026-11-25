package main

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func executeCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestAcceptanceDemoFailuresMappingsAndBudget(t *testing.T) {
	code, out, errOut := executeCLI(t, "demo", "--seed", "17", "--workers", "3", "--budget-bytes", "1048576")
	if code != exitExpectedFail {
		t.Fatalf("exit code=%d, want 2; stderr=%s\nstdout tail=%s", code, errOut, tail(out, 500))
	}

	required := []string{
		"input=sample-00-srt-valid.in output=out/sample-00-srt-valid.out status=success",
		"time_delta_ns=0 byte_delta=0",
		"phase=subtitle code=TRUNCATED_INPUT sample=7 offset=2",
		"phase=clock code=ILLEGAL_DROP_FRAME sample=8 offset=9",
		"phase=layout code=METADATA_CONFLICT sample=9 offset=256",
		"phase=rtp code=BAD_RTP_TIMESTAMP sample=10 offset=4",
		"code=LINE_TOO_LARGE sample=11 offset=4098",
		"phase=policy code=BUDGET_INFEASIBLE sample=12 offset=320",
	}
	for _, want := range required {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\n%s", want, out)
		}
	}

	successCount := 0
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "item ") || !strings.Contains(line, "status=success") {
			continue
		}
		successCount++
		fields := strings.Fields(line)
		for _, prefix := range []string{"time_delta_ns=", "byte_delta="} {
			for _, field := range fields {
				if strings.HasPrefix(field, prefix) && field != prefix+"0" {
					t.Fatalf("nonzero deviation in line: %s", line)
				}
			}
		}
	}
	if successCount != 7 {
		t.Fatalf("success mappings=%d, want 7", successCount)
	}

	re := regexp.MustCompile(`budget budget_bytes=1048576 total_output_bytes=(\d+) within=true`)
	m := re.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("missing within-budget summary:\n%s", out)
	}
	total, _ := strconv.Atoi(m[1])
	if total <= 0 || total > 1048576 {
		t.Fatalf("total output bytes=%d", total)
	}
}

func TestAcceptanceWorkerCountsHaveSameDigestAndMappings(t *testing.T) {
	_, out3, _ := executeCLI(t, "demo", "--seed", "17", "--workers", "3", "--budget-bytes", "1048576")
	_, out1, _ := executeCLI(t, "demo", "--seed", "17", "--workers", "1", "--budget-bytes", "1048576")

	d3 := stableDigest(t, out3)
	d1 := stableDigest(t, out1)
	if d3 != d1 {
		t.Fatalf("digests differ: workers3=%s workers1=%s", d3, d1)
	}
	if strings.Join(itemLines(out3), "\n") != strings.Join(itemLines(out1), "\n") {
		t.Fatalf("item mappings differ:\nworkers3:\n%s\nworkers1:\n%s",
			strings.Join(itemLines(out3), "\n"), strings.Join(itemLines(out1), "\n"))
	}
}

func TestAcceptanceCancelAfterTwoResumesToFullDigest(t *testing.T) {
	code, out, errOut := executeCLI(t, "demo", "--seed", "17", "--workers", "3", "--cancel-after", "2")
	if code != exitExpectedFail {
		t.Fatalf("cancel demo code=%d stderr=%s", code, errOut)
	}
	re := regexp.MustCompile(`cancel_run total=13 completed=2 canceled=(\d+) not_started=(\d+)`)
	m := re.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("missing cancel counts:\n%s", out)
	}
	canceled, _ := strconv.Atoi(m[1])
	notStarted, _ := strconv.Atoi(m[2])
	if canceled == 0 || notStarted == 0 {
		t.Fatalf("canceled=%d not_started=%d, want both positive", canceled, notStarted)
	}
	resume := regexp.MustCompile(`resume_digest=([0-9a-f]{64})`).FindStringSubmatch(out)
	full := regexp.MustCompile(`full_digest=([0-9a-f]{64})`).FindStringSubmatch(out)
	if resume == nil || full == nil || resume[1] != full[1] {
		t.Fatalf("digest mismatch resume=%v full=%v\n%s", resume, full, out)
	}
	if !strings.Contains(out, "resume_matches_full=true") {
		t.Fatal("missing resume match marker")
	}
}

func TestAcceptanceUsageAndSuccessCodes(t *testing.T) {
	if code, _, _ := executeCLI(t, "unknown"); code != exitUsage {
		t.Fatalf("unknown command code=%d, want 64", code)
	}
	if code, _, _ := executeCLI(t, "demo", "--workers", "64"); code != exitUsage {
		t.Fatalf("bad workers code=%d, want 64", code)
	}
	if code, out, errOut := executeCLI(t, "demo", "--valid-only", "--workers", "2"); code != exitOK {
		t.Fatalf("valid-only code=%d stderr=%s\n%s", code, errOut, out)
	}
}

func stableDigest(t *testing.T, out string) string {
	t.Helper()
	m := regexp.MustCompile(`stable_digest=([0-9a-f]{64})`).FindStringSubmatch(out)
	if m == nil {
		t.Fatal("stable digest not found")
	}
	return m[1]
}

func itemLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "item ") {
			lines = append(lines, line)
		}
	}
	return lines
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
