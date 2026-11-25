package video

import (
	"testing"

	"replay/internal/model"
)

func TestPlanColorMissing(t *testing.T) {
	rep := &model.Report{}
	p := PlanColor(nil, rep)
	if p.Convert {
		t.Fatal("missing metadata must never be reported as converted")
	}
	if p.Source != nil {
		t.Fatal("source must stay nil")
	}
	if rep.Count(model.CodeColorMissing) != 1 {
		t.Fatal("expected COLOR_MISSING issue")
	}
}

func TestPlanColorMatching(t *testing.T) {
	rep := &model.Report{}
	src := TargetBT709
	p := PlanColor(&src, rep)
	if p.Convert {
		t.Fatal("matching input must not be converted")
	}
	if len(rep.Issues) != 0 {
		t.Fatalf("no issues expected: %+v", rep.Issues)
	}
}

func TestPlanColorConflict(t *testing.T) {
	rep := &model.Report{}
	src := model.ColorInfo{Primaries: "bt2020", Transfer: "bt2020", Matrix: "bt2020"}
	p := PlanColor(&src, rep)
	if !p.Convert {
		t.Fatal("conflicting metadata must trigger conversion")
	}
	if p.Source == nil || p.Source.Primaries != "bt2020" {
		t.Fatal("source must be recorded for audit")
	}
	if p.Target != TargetBT709 {
		t.Fatal("target must be bt709")
	}
	if rep.Count(model.CodeColorConverted) != 1 {
		t.Fatal("expected COLOR_CONVERTED issue")
	}
}
