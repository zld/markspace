package metrics

import (
	"strings"
	"testing"

	"continuity/internal/core"
)

func sampleItems() []core.Item {
	return []core.Item{
		{Index: 0, Source: "seg0.m4s", Line: 5, Output: 0, Decision: core.DecEmitted,
			MediaRef: 90000, ExpectedRef: 90000, Fingerprint: [32]byte{1, 2, 3}},
		{Index: 1, Source: "seg1.m4s", Line: 7, Output: 1, Decision: core.DecEmitted,
			MediaRef: 180000, ExpectedRef: 180000, TimeDeltaUs: 11, DelayMs: 40,
			Fingerprint: [32]byte{4, 5, 6}},
	}
}

func TestDigestStableAndHex64(t *testing.T) {
	d1 := Digest(sampleItems())
	d2 := Digest(sampleItems())
	if d1 != d2 {
		t.Fatalf("digest not deterministic: %s vs %s", d1, d2)
	}
	if len(d1) != 64 {
		t.Fatalf("digest len=%d, want 64", len(d1))
	}
	for _, c := range d1 {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("non-hex char in digest: %c", c)
		}
	}
}

func TestDigestChangesWithContent(t *testing.T) {
	a := sampleItems()
	b := sampleItems()
	b[1].TimeDeltaUs = 12
	if Digest(a) == Digest(b) {
		t.Fatal("digest should change with content")
	}
}

func TestReportLayout(t *testing.T) {
	var sb strings.Builder
	res := &core.Result{
		Items:   sampleItems(),
		Summary: core.Summary{Mapped: 2, Emitted: 2, P50DelayMs: 40, DriftPPM: 80},
	}
	Report(&sb, res)
	lines := strings.Split(strings.TrimRight(sb.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines=%d, want 3", len(lines))
	}
	if !strings.HasPrefix(lines[0], "map idx=0 source=seg0.m4s") {
		t.Fatalf("line0: %s", lines[0])
	}
	if !strings.HasPrefix(lines[2], "summary mapped=2 emitted=2") {
		t.Fatalf("summary: %s", lines[2])
	}
	if !strings.Contains(lines[2], "digest=") {
		t.Fatal("summary missing digest")
	}
}
