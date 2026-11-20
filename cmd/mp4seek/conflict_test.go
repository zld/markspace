package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"mp4seek/internal/bmff"
	"mp4seek/internal/index"
)

// TestStssConflictDiagnostic corrupts sample 1's NAL header so stss and
// the actual IDR classification disagree. The conflicted sample must be
// reported as a diagnostic and must not be used as the anchor.
func TestStssConflictDiagnostic(t *testing.T) {
	p := fixturePath(t, "edit-wrap")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// Locate sample 1 via the real index and flip its NAL header from
	// IDR (0x65) to non-IDR (0x61).
	r := bmff.NewReader(bytes.NewReader(data), int64(len(data)))
	mv, err := loadMovie(r)
	if err != nil {
		t.Fatal(err)
	}
	ti, err := mv.track(1)
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := index.BuildClassic(r, ti.Stbl)
	if err != nil {
		t.Fatal(err)
	}
	nalOff := tbl.Samples[1].Offset + 4 // skip 4-byte length prefix
	if data[nalOff] != 0x65 {
		t.Fatalf("expected IDR NAL header, got %#x", data[nalOff])
	}
	data[nalOff] = 0x61
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runCLI(t, "seek", "--input", p, "--track", "1", "--at-ms", "250")
	if code != 0 {
		t.Fatalf("seek exit %d: %s", code, errOut)
	}
	if !strings.Contains(errOut, "diagnostic: sample=1") {
		t.Fatalf("missing conflict diagnostic:\n%s", errOut)
	}
	// Sample 1 is excluded; the anchor falls back to sample 0.
	if !strings.Contains(out, "anchor_sample=0") {
		t.Fatalf("anchor should fall back to sample 0:\n%s", out)
	}
}
