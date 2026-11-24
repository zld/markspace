package fixture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"archive/internal/model"
)

func TestGenerateWritesCleanAndDamaged(t *testing.T) {
	dir := t.TempDir()
	specs := DefaultMixed()
	set, err := Generate(dir, specs, []DamageKind{DamageTruncate, DamageSegment, DamageIndex})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Artifacts) != len(specs)*2 {
		t.Fatalf("artifacts = %d, want %d", len(set.Artifacts), len(specs)*2)
	}
	for _, spec := range specs {
		clean := set.Clean(spec.Name)
		if clean == nil {
			t.Fatalf("no clean artifact for %s", spec.Name)
		}
		if clean.Size == 0 || clean.SHA256 == "" {
			t.Fatalf("clean artifact incomplete: %+v", clean)
		}
		damaged := set.Damaged(spec.Name)
		if damaged == nil {
			t.Fatalf("no damaged artifact for %s", spec.Name)
		}
		if damaged.Size >= clean.Size {
			t.Fatalf("%s: damaged file not smaller (%d vs %d)", spec.Name, damaged.Size, clean.Size)
		}
		if len(damaged.Injections) == 0 {
			t.Fatalf("%s: no injections recorded", spec.Name)
		}
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("manifest is not valid json: %v", err)
	}
	if len(entries) != len(set.Artifacts) {
		t.Fatalf("manifest entries = %d", len(entries))
	}
}

func TestGenerateIsReproducible(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	specs := DefaultMixed()
	setA, err := Generate(dirA, specs, nil)
	if err != nil {
		t.Fatal(err)
	}
	setB, err := Generate(dirB, specs, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range setA.Artifacts {
		if setA.Artifacts[i].SHA256 != setB.Artifacts[i].SHA256 {
			t.Fatalf("artifact %d differs between runs", i)
		}
	}
}

func TestDamagePlanTruncateCoversLastFrame(t *testing.T) {
	s, _ := FindSpec(DefaultMixed(), "clip_b")
	layout, data, err := BuildMP4(s)
	if err != nil {
		t.Fatal(err)
	}
	plan, rebuild, err := Plan(layout, []DamageKind{DamageTruncate})
	if err != nil {
		t.Fatal(err)
	}
	if rebuild {
		t.Fatal("truncate must not require rebuilding")
	}
	if len(plan) != 1 || plan[0].Kind != DamageTruncate {
		t.Fatalf("plan = %+v", plan)
	}
	damaged, injections, err := Apply(layout, data, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(damaged) != int(plan[0].At) {
		t.Fatalf("damaged size = %d, want %d", len(damaged), plan[0].At)
	}
	if len(injections) != 1 || !injections[0].HasRange {
		t.Fatalf("injections = %+v", injections)
	}
	// The injection range must be a real frame's range.
	found := false
	for _, tl := range layout.Tracks {
		for _, f := range tl.Frames {
			if f.StartTick == injections[0].StartTick && f.StartTick+f.Duration == injections[0].EndTick {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("injected range does not match a frame: %+v", injections[0])
	}
}

func TestDamageSegmentHitsFrame(t *testing.T) {
	s, _ := FindSpec(DefaultMixed(), "reel_a")
	layout, data, err := BuildMXF(s)
	if err != nil {
		t.Fatal(err)
	}
	plan, _, err := Plan(layout, []DamageKind{DamageSegment})
	if err != nil {
		t.Fatal(err)
	}
	_, injections, err := Apply(layout, data, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(injections) != 1 || !injections[0].HasRange || injections[0].Timescale == 0 {
		t.Fatalf("injections = %+v", injections)
	}
	if injections[0].EndTick <= injections[0].StartTick {
		t.Fatalf("empty range: %+v", injections[0])
	}
}

func TestParseDamageKinds(t *testing.T) {
	got, err := ParseDamageKinds("truncate, segment,index,segment")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("kinds = %v", got)
	}
	if _, err := ParseDamageKinds("nonsense"); err == nil {
		t.Fatal("expected error")
	}
	if kinds, err := ParseDamageKinds("  "); err != nil || len(kinds) != 0 {
		t.Fatalf("empty flag = %v, %v", kinds, err)
	}
	if !containsKind(got, DamageIndex) {
		t.Fatal("index missing")
	}
}

func TestDropIndexesChangesSize(t *testing.T) {
	specs := DefaultMixed()
	for _, s := range specs {
		dropped := dropIndexes(s)
		_, full, err := build(s)
		if err != nil {
			t.Fatal(err)
		}
		_, lean, err := build(dropped)
		if err != nil {
			t.Fatal(err)
		}
		if len(lean) >= len(full) {
			t.Fatalf("%s: dropping index did not shrink the file", s.Name)
		}
	}
	_ = model.ContainerMP4
}
