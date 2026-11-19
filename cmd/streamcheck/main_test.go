package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"streamcheck/internal/report"
)

func invoke(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestNoArgsIsUsageError(t *testing.T) {
	code, _, stderr := invoke(t)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Fatalf("usage not printed:\n%s", stderr)
	}
}

func TestUnknownCommandIsUsageError(t *testing.T) {
	code, _, stderr := invoke(t, "frobnicate")
	if code != 2 || !strings.Contains(stderr, "unknown command") {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
}

func TestMissingRootIsUsageError(t *testing.T) {
	for _, command := range []string{"build", "verify", "demo"} {
		code, _, stderr := invoke(t, command)
		if code != 2 {
			t.Errorf("%s exit = %d, want 2", command, code)
		}
		if !strings.Contains(stderr, "--root is required") {
			t.Errorf("%s stderr = %s", command, stderr)
		}
	}
}

func TestBadFlagsAreUsageErrors(t *testing.T) {
	cases := [][]string{
		{"verify", "--root", t.TempDir(), "--missing-tolerance", "-1"},
		{"verify", "--root", t.TempDir(), "--deviation-ms", "0"},
		{"demo", "--root", t.TempDir(), "--rates", "nope"},
		{"demo", "--root", t.TempDir(), "--rates", "800000", "--drop", "1600000:1"},
		{"demo", "--root", t.TempDir(), "--rates", "800000", "--drop", "800000:99"},
		{"demo", "--root", t.TempDir(), "--rates", "800000", "--shuffle", "1600000"},
		{"build", "--root", t.TempDir(), "--rates", "0"},
	}
	for _, args := range cases {
		if code, _, _ := invoke(t, args...); code != 2 {
			t.Errorf("%v: exit = %d, want 2", args, code)
		}
	}
}

func TestNonexistentRootIsFileError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-there")
	for _, command := range []string{"build", "verify"} {
		code, _, stderr := invoke(t, command, "--root", missing)
		if code != 2 || !strings.Contains(stderr, "error:") {
			t.Errorf("%s: exit = %d, stderr = %s", command, code, stderr)
		}
	}
}

func TestVerifyNonexistentRootJSON(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-there")
	code, stdout, _ := invoke(t, "verify", "--root", missing, "--json")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	var doc report.Error
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not a JSON error: %v\n%s", err, stdout)
	}
	if doc.Status != "error" {
		t.Fatalf("status = %s", doc.Status)
	}
}

func TestDemoThenVerify(t *testing.T) {
	root := t.TempDir()
	code, stdout, stderr := invoke(t, "demo",
		"--root", root, "--rates", "800000,1600000,3200000", "--segments", "12",
		"--drop", "1600000:5", "--shuffle", "3200000")
	if code != 0 {
		t.Fatalf("demo exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "rate_count=3") {
		t.Fatalf("demo stdout = %s", stdout)
	}

	code, stdout, stderr = invoke(t, "verify", "--root", root, "--json")
	if code != 1 {
		t.Fatalf("verify exit = %d, want 1\nstderr: %s", code, stderr)
	}
	var doc report.Verification
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("verify stdout is not JSON: %v\n%s", err, stdout)
	}
	if doc.RateCount != 3 || doc.MissingCount != 1 || doc.OutOfOrderCount == 0 {
		t.Fatalf("unexpected summary: %+v", doc)
	}
}

func TestVerifyCleanDirectoryPasses(t *testing.T) {
	root := t.TempDir()
	if code, _, stderr := invoke(t, "demo", "--root", root, "--rates", "800000,1600000", "--segments", "6"); code != 0 {
		t.Fatalf("demo failed: %s", stderr)
	}
	code, stdout, _ := invoke(t, "verify", "--root", root)
	if code != 0 {
		t.Fatalf("verify exit = %d\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "status=pass") {
		t.Fatalf("stdout = %s", stdout)
	}
}

func TestToleranceChangesExitCode(t *testing.T) {
	root := t.TempDir()
	if code, _, stderr := invoke(t, "demo", "--root", root, "--rates", "800000,1600000", "--segments", "6",
		"--drop", "800000:2", "--drop", "1600000:2"); code != 0 {
		t.Fatalf("demo failed: %s", stderr)
	}
	if code, _, _ := invoke(t, "verify", "--root", root); code != 1 {
		t.Fatalf("strict verify exit = %d, want 1", code)
	}
	if code, _, _ := invoke(t, "verify", "--root", root, "--missing-tolerance", "2"); code != 0 {
		t.Fatalf("tolerant verify exit = %d, want 0", code)
	}
}

func TestBuildWritesManifests(t *testing.T) {
	root := t.TempDir()
	if code, _, stderr := invoke(t, "demo", "--root", root, "--rates", "800000", "--segments", "4"); code != 0 {
		t.Fatalf("demo failed: %s", stderr)
	}
	if err := os.Remove(filepath.Join(root, "800000.m3u8")); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := invoke(t, "build", "--root", root)
	if code != 0 {
		t.Fatalf("build exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "status=built") {
		t.Fatalf("stdout = %s", stdout)
	}
	body, err := os.ReadFile(filepath.Join(root, "800000.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "#EXT-X-ENDLIST") || !strings.Contains(string(body), "#EXT-X-TARGETDURATION:2") {
		t.Fatalf("manifest looks wrong:\n%s", body)
	}
	if code, _, _ := invoke(t, "verify", "--root", root); code != 0 {
		t.Fatal("built manifest should verify clean")
	}
}

func TestRateFilterLimitsVerification(t *testing.T) {
	root := t.TempDir()
	if code, _, stderr := invoke(t, "demo", "--root", root, "--rates", "800000,1600000", "--segments", "6",
		"--drop", "1600000:2"); code != 0 {
		t.Fatalf("demo failed: %s", stderr)
	}
	code, stdout, _ := invoke(t, "verify", "--root", root, "--rates", "800000", "--json")
	if code != 0 {
		t.Fatalf("filtered verify exit = %d\n%s", code, stdout)
	}
	var doc report.Verification
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.RateCount != 1 || doc.Rates[0].Rate != 800000 {
		t.Fatalf("rate filter ignored: %+v", doc)
	}
}

func TestCaseInsensitiveExtension(t *testing.T) {
	root := t.TempDir()
	if code, _, stderr := invoke(t, "demo", "--root", root, "--rates", "800000", "--segments", "2"); code != 0 {
		t.Fatalf("demo failed: %s", stderr)
	}
	if err := os.Rename(filepath.Join(root, "seg_800000_0_1000.ts"), filepath.Join(root, "seg_800000_0_1000.TS")); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := invoke(t, "build", "--root", root); code != 0 {
		t.Fatalf("build should accept .TS: %s", stderr)
	}
}
