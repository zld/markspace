package demo

import (
	"os"
	"path/filepath"
	"testing"

	"dashgen/internal/plan"
	"dashgen/internal/timebase"
)

func TestBuildShape(t *testing.T) {
	p := Build()
	if err := p.Validate(); err != nil {
		t.Fatalf("demo plan is invalid: %v", err)
	}
	if len(p.Representations) != 3 {
		t.Fatalf("representations = %d, want 3", len(p.Representations))
	}
	for _, rep := range p.Representations {
		if len(rep.Segments) != 6 {
			t.Fatalf("%s segments = %d, want 6", rep.ID, len(rep.Segments))
		}
	}
}

// TestBuildOffsets reproduces the acceptance metrics: max start offset 12.5ms,
// max duration delta 1.0ms across the three renditions.
func TestBuildOffsets(t *testing.T) {
	p := Build()
	starts := map[int64][]timebase.Rational{}
	durs := map[int64][]timebase.Rational{}
	for _, rep := range p.Representations {
		for _, seg := range rep.Segments {
			s, err := timebase.TicksToMs(seg.StartTicks, rep.Timescale)
			if err != nil {
				t.Fatal(err)
			}
			d, err := timebase.TicksToMs(seg.DurationTicks, rep.Timescale)
			if err != nil {
				t.Fatal(err)
			}
			starts[seg.Number] = append(starts[seg.Number], s)
			durs[seg.Number] = append(durs[seg.Number], d)
		}
	}
	maxStart := timebase.Rational{}
	maxDur := timebase.Rational{}
	found := false
	for n := int64(1); n <= 6; n++ {
		ss := starts[n]
		dd := durs[n]
		if len(ss) != 3 {
			t.Fatalf("segment %d present in %d reps, want 3", n, len(ss))
		}
		for i := 0; i < 3; i++ {
			for j := i + 1; j < 3; j++ {
				diff, err := ss[i].Sub(ss[j])
				if err != nil {
					t.Fatal(err)
				}
				ddiff, err := dd[i].Sub(dd[j])
				if err != nil {
					t.Fatal(err)
				}
				if !found {
					maxStart, maxDur, found = diff.Abs(), ddiff.Abs(), true
					continue
				}
				if diff.Abs().Cmp(maxStart) > 0 {
					maxStart = diff.Abs()
				}
				if ddiff.Abs().Cmp(maxDur) > 0 {
					maxDur = ddiff.Abs()
				}
			}
		}
	}
	if got := timebase.FormatMillis(maxStart); got != "12.500" {
		t.Fatalf("max start offset = %s ms, want 12.500", got)
	}
	if got := timebase.FormatMillis(maxDur); got != "1.000" {
		t.Fatalf("max duration delta = %s ms, want 1.000", got)
	}
}

func TestWritePlan(t *testing.T) {
	dir := t.TempDir()
	path, err := WritePlan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, PlanFileName); filepath.Base(path) != filepath.Base(want) {
		t.Fatalf("wrote %s, want basename %s", path, want)
	}
	loaded, err := plan.ReadFile(path)
	if err != nil {
		t.Fatalf("written plan cannot be re-read: %v", err)
	}
	if len(loaded.Representations) != 3 {
		t.Fatalf("re-read reps = %d, want 3", len(loaded.Representations))
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("plan file missing: %v", err)
	}
}
