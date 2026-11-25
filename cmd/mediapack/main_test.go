package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLosses(t *testing.T) {
	losses, err := parseLosses("0, 3,4")
	if err != nil {
		t.Fatal(err)
	}
	if len(losses) != 3 || losses[1] != 3 {
		t.Fatalf("unexpected losses: %v", losses)
	}
	if _, err := parseLosses("nope"); err == nil {
		t.Fatal("expected invalid loss error")
	}
}

func TestCLIDemoJSONSuccessAndBadFlag(t *testing.T) {
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	code := run([]string{
		"mediapack", "demo", "--seed", "17", "--bitrates", "2",
		"--queue-capacity", "8", "--report", "json",
		"--output", filepath.Join(t.TempDir(), "cli"),
	}, out, errOut)
	if code != 0 {
		t.Fatalf("expected success code 0, got %d stderr=%s stdout=%s", code, errOut.String(), out.String())
	}
	var report map[string]interface{}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("stdout was not JSON: %v\n%s", err, out.String())
	}
	if report["manifest_sha256"] == "" {
		t.Fatal("missing manifest summary")
	}

	code = run([]string{"mediapack", "demo", "--bitrates", "1"}, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 2 {
		t.Fatalf("expected parameter error code 2, got %d", code)
	}
}

func TestCLIUsageAndTextReport(t *testing.T) {
	if code := run([]string{"mediapack"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
		t.Fatalf("usage should exit 2, got %d", code)
	}
	out := &bytes.Buffer{}
	code := run([]string{
		"mediapack", "demo", "--report", "text",
		"--output", filepath.Join(t.TempDir(), "text"),
	}, out, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("text demo failed code=%d", code)
	}
	if !strings.Contains(out.String(), "status=ok") {
		t.Fatalf("unexpected text output: %s", out.String())
	}
}
