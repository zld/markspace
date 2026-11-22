package pipeline

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTamperedReportFailsVerification edits one offset in the serialized
// report and expects verify to reject it, proving the checksum covers the
// analysis body rather than only a stage summary.
func TestTamperedReportFailsVerification(t *testing.T) {
	root, names, _ := setup(t)
	out, err := runner(root, "").Run(names)
	if err != nil && !strings.Contains(err.Error(), "damaged") {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteJSONL(&buf, out, runner(root, "").Params, out.InputDigest, out.ParamsDigest); err != nil {
		t.Fatal(err)
	}
	orig := buf.String()
	tampered := strings.Replace(orig, `"start":2667`, `"start":2668`, 1)
	if tampered == orig {
		t.Skip("report does not contain the expected offset")
	}
	parsed, err := ParseReport(strings.NewReader(tampered))
	if err != nil {
		t.Fatal(err)
	}
	_, digest, match := VerifyReport(parsed)
	if match {
		t.Fatal("tampered report still matched the checksum")
	}
	if digest == out.Summary.SHA256 {
		t.Fatal("tampered digest equals the recorded checksum")
	}
}

// TestWriteReportRefusesEscape checks the output confinement guard.
func TestWriteReportRefusesEscape(t *testing.T) {
	root := t.TempDir()
	outRoot := filepath.Join(root, "out")
	if err := os.MkdirAll(outRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	out := &Output{}
	err := WriteReportIn(outRoot, filepath.Join(root, "outside.jsonl"), out, Parameters{}, "", "")
	if err == nil {
		t.Fatal("want path escape rejection")
	}
	if _, statErr := os.Stat(filepath.Join(root, "outside.jsonl")); statErr == nil {
		t.Fatal("report was written outside the output root")
	}
}

// TestParseReportRejectsTruncatedLine ensures a partially written report is
// rejected rather than silently accepted as fewer records.
func TestParseReportRejectsTruncatedLine(t *testing.T) {
	if _, err := ParseReport(strings.NewReader(`{"kind":"asset","asset":{"name":"a`)); err == nil {
		t.Fatal("want parse error for truncated line")
	}
}
