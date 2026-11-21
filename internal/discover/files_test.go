package discover

import (
	"os"
	"path/filepath"
	"testing"

	"streamcheck/internal/manifest"
)

func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func mustPattern(t *testing.T, expr string) *manifest.Pattern {
	t.Helper()
	p, err := manifest.CompilePattern(expr)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScanSortsNumericallyAndReportsDuplicates(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir,
		"seg_1600_10.ts", "seg_1600_2.ts", "seg_800_1.ts", "seg_800_0.ts",
		"seg_800_2.ts", "seg_800_02.ts", "notes.txt", ".hidden", "seg_800_0.copy.ts")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := Scan(dir, Options{Pattern: mustPattern(t, manifest.DefaultPattern), RateFilter: []int{800}})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.Segments) != 2 {
		t.Fatalf("segments = %+v", res.Segments)
	}
	if res.Segments[0].ID != 0 || res.Segments[1].ID != 1 {
		t.Fatalf("segments not sorted by id: %+v", res.Segments)
	}
	if got := res.DuplicateIDs(800); len(got) != 1 || got[0] != 2 {
		t.Fatalf("duplicate ids = %v, want [2]", got)
	}
	names := res.Duplicates[800][2]
	want := []string{filepath.Join(dir, "seg_800_02.ts"), filepath.Join(dir, "seg_800_2.ts")}
	if len(names) != 2 || names[0] != want[0] || names[1] != want[1] {
		t.Fatalf("duplicate paths = %v, want %v", names, want)
	}
	if res.Ignored != 3 {
		t.Fatalf("ignored = %d, want 3", res.Ignored)
	}
	if len(res.OtherRates) != 1 || res.OtherRates[0] != 1600 {
		t.Fatalf("other rates = %v", res.OtherRates)
	}
	if res.Segments[0].SizeBytes != int64(len("seg_800_0.ts")) {
		t.Fatalf("size = %d", res.Segments[0].SizeBytes)
	}
}

func TestScanRejectsBadInput(t *testing.T) {
	if _, err := Scan(filepath.Join(t.TempDir(), "missing"), Options{Pattern: mustPattern(t, manifest.DefaultPattern)}); err == nil {
		t.Fatal("Scan accepted missing directory")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(file, Options{Pattern: mustPattern(t, manifest.DefaultPattern)}); err == nil {
		t.Fatal("Scan accepted a regular file")
	}
}
