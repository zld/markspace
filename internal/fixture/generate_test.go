package fixture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mediarc/internal/index"
	"mediarc/internal/scan"
)

func TestGenerateInvariants(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(dir, 17); err != nil {
		t.Fatalf("generate: %v", err)
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	// truncated stream is exactly 41 bytes and scans to 1 AU + error at 41
	tr := read("trunc.h264")
	if len(tr) != TruncSize {
		t.Fatalf("trunc len = %d, want %d", len(tr), TruncSize)
	}
	aus, serr := scan.ScanNAL(tr, scan.CodecH264)
	if serr == nil || serr.Offset != TruncSize || len(aus) != 1 {
		t.Fatalf("trunc scan: aus=%d err=%v", len(aus), serr)
	}
	// version streams scan cleanly into the expected AU counts
	for name, want := range map[string]int{"va.h264": Positions, "vb.h264": Positions - 1} {
		aus, serr := scan.ScanNAL(read(name), scan.CodecH264)
		if serr != nil || len(aus) != want {
			t.Fatalf("%s: aus=%d err=%v, want %d", name, len(aus), serr, want)
		}
	}
	// indexes are well-formed and reference their streams
	for name, want := range map[string]int{"va.index.json": 6, "vb.index.json": 5, "bad.index.json": 2} {
		var f index.File
		if err := json.Unmarshal(read(name), &f); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(f.Records) != want {
			t.Fatalf("%s: %d records, want %d", name, len(f.Records), want)
		}
	}
	// deterministic: regenerating with the same seed yields identical bytes
	dir2 := t.TempDir()
	if err := Generate(dir2, 17); err != nil {
		t.Fatal(err)
	}
	b1, _ := os.ReadFile(filepath.Join(dir, "va.h264"))
	b2, _ := os.ReadFile(filepath.Join(dir2, "va.h264"))
	if string(b1) != string(b2) {
		t.Fatal("fixture not deterministic")
	}
}
