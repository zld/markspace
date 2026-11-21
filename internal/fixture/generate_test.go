package fixture

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"streamcheck/internal/discover"
	"streamcheck/internal/manifest"
	"streamcheck/internal/probe"
)

func TestGenerateLayoutAndDeterminism(t *testing.T) {
	out := t.TempDir()
	spec := Spec{Out: out, Rates: []int{800, 1600}, Segments: 6, SegmentSeconds: 4, Pattern: manifest.DefaultPattern}
	if err := Generate(spec); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, name := range []string{"cases/missing", "cases/duplicate", "cases/unordered", "cases/drift"} {
		info, err := os.Stat(filepath.Join(out, name))
		if err != nil || !info.IsDir() {
			t.Fatalf("case %s: %v", name, err)
		}
	}
	for _, name := range []string{"seg_800_0.ts", "seg_800_5.ts", "seg_1600_0.ts"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	sizes := map[string]bool{}
	for _, name := range []string{"seg_800_0.ts", "seg_1600_0.ts"} {
		fi, err := os.Stat(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		sizes[name] = true
		_ = fi
	}
	lo, _ := os.Stat(filepath.Join(out, "seg_800_0.ts"))
	hi, _ := os.Stat(filepath.Join(out, "seg_1600_0.ts"))
	if hi.Size() <= lo.Size() {
		t.Fatalf("higher bitrate file is not larger: %d vs %d", hi.Size(), lo.Size())
	}

	first, err := os.ReadFile(filepath.Join(out, "seg_800_0.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Generate(spec); err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(out, "seg_800_0.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("fixture is not deterministic")
	}
}

func TestFixtureDurations(t *testing.T) {
	if err := probe.Available(""); err != nil {
		t.Skip(err)
	}
	out := t.TempDir()
	spec := Spec{Out: out, Rates: []int{800, 1600}, Segments: 6, SegmentSeconds: 4, Pattern: manifest.DefaultPattern}
	if err := Generate(spec); err != nil {
		t.Fatal(err)
	}
	pattern, err := manifest.CompilePattern(manifest.DefaultPattern)
	if err != nil {
		t.Fatal(err)
	}
	res, err := discover.Scan(out, discover.Options{Pattern: pattern})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) != 12 || len(res.Duplicates) != 0 {
		t.Fatalf("scanned %d segments, duplicates %v", len(res.Segments), res.Duplicates)
	}
	probe.Run(context.Background(), res.Segments, probe.Options{Timeout: 5e9, Concurrency: 4})
	for _, seg := range res.Segments {
		if seg.ProbeError != "" {
			t.Fatalf("%s: %s", seg.FileName, seg.ProbeError)
		}
		if seg.DurationMS < 3990 || seg.DurationMS > 4010 {
			t.Fatalf("%s duration = %dms", seg.FileName, seg.DurationMS)
		}
	}

	// Cases must contain exactly the intended anomalies.
	missing, _ := discover.Scan(filepath.Join(out, "cases", "missing"), discover.Options{Pattern: pattern})
	if len(missing.Segments) != 8 {
		t.Fatalf("missing case has %d segments, want 8", len(missing.Segments))
	}
	dupe, _ := discover.Scan(filepath.Join(out, "cases", "duplicate"), discover.Options{Pattern: pattern})
	if len(dupe.Duplicates) != 2 {
		t.Fatalf("duplicate case duplicates = %v", dupe.Duplicates)
	}
	drift, _ := discover.Scan(filepath.Join(out, "cases", "drift"), discover.Options{Pattern: pattern})
	if len(drift.Segments) != 12 {
		t.Fatalf("drift case has %d segments", len(drift.Segments))
	}
}

func TestGenerateValidation(t *testing.T) {
	for _, spec := range []Spec{
		{},
		{Out: t.TempDir(), Rates: []int{800}, Segments: 0, SegmentSeconds: 4, Pattern: manifest.DefaultPattern},
		{Out: t.TempDir(), Rates: []int{800}, Segments: 1, SegmentSeconds: 0, Pattern: manifest.DefaultPattern},
		{Out: t.TempDir(), Rates: []int{800, 1600}, Segments: 1, SegmentSeconds: 4, Pattern: "chunk_{index}.ts"},
	} {
		if err := Generate(spec); err == nil {
			t.Errorf("Generate(%+v) succeeded, want error", spec)
		}
	}
}
