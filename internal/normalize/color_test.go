package normalize

import (
	"testing"

	"mediaqc/internal/model"
)

func TestPriorityResolution(t *testing.T) {
	p := NewColorPolicy("mat-c")
	descs := []ColorDesc{
		{Primaries: 1, Transfer: 1, Matrix: 1, Source: SourceManifest, Raw: "manifest=bt709"},
		{Primaries: 9, Transfer: 16, Matrix: 9, Source: SourceContainer, Raw: "colr=bt2020"},
		{Primaries: 9, Transfer: 16, Matrix: 9, Source: SourceBitstream, Raw: "vui=bt2020"},
	}
	dec, err := p.Reconcile("v", descs)
	// Conflict exists (bt709 vs bt2020) but it is resolvable by priority.
	if err == nil || !model.HasCode(err, model.CodeColorConflict) {
		t.Fatalf("conflicting known descs must be reported, got %v", err)
	}
	if !model.IsRecoverable(err) {
		t.Fatal("priority-resolvable color conflict should be recoverable")
	}
	if dec.Selected.Source != SourceContainer || dec.Selected.Primaries != 9 {
		t.Fatalf("container must win over manifest/bitstream: %+v", dec.Selected)
	}
	if len(dec.AllDeclared) != 3 {
		t.Fatal("all original declarations must be preserved")
	}
	if dec.AllDeclared[0].Raw != "manifest=bt709" {
		t.Fatal("original declaration order/raw text must be retained")
	}
}

func TestUnknownColorQuarantinedNotDefaulted(t *testing.T) {
	p := NewColorPolicy("mat-c2")
	descs := []ColorDesc{
		{Primaries: 999, Transfer: 1, Matrix: 1, Source: SourceBitstream, Raw: "vui=code999"},
	}
	dec, err := p.Reconcile("v", descs)
	if err == nil || !model.HasCode(err, model.CodeUnknownColor) {
		t.Fatalf("all-unknown must raise unknown_color, got %v", err)
	}
	if len(dec.Quarantined) != 1 {
		t.Fatal("unknown description must be quarantined")
	}
	// It must not have been rewritten into a known/safe value.
	if dec.Quarantined[0].Primaries != 999 {
		t.Fatal("unknown code must be preserved, not replaced with a default")
	}
	if dec.Selected.Primaries != 0 {
		t.Fatal("no safe default may be fabricated")
	}
}

func TestUnknownQuarantinedWhileKnownSelected(t *testing.T) {
	p := NewColorPolicy("mat-c3")
	descs := []ColorDesc{
		{Primaries: 1, Transfer: 1, Matrix: 1, Source: SourceContainer, Raw: "colr=709"},
		{Primaries: 4242, Transfer: 1, Matrix: 1, Source: SourceManifest, Raw: "x-weird"},
	}
	dec, err := p.Reconcile("v", descs)
	if err != nil {
		t.Fatalf("one known + one unknown should resolve: %v", err)
	}
	if dec.Selected.Primaries != 1 || len(dec.Quarantined) != 1 {
		t.Fatalf("known selected, unknown quarantined: %+v", dec)
	}
}

func TestNoDeclarationsRejected(t *testing.T) {
	_, err := NewColorPolicy("mat-c4").Reconcile("v", nil)
	if err == nil || !model.HasCode(err, model.CodeColorConflict) {
		t.Fatalf("empty color metadata must error, got %v", err)
	}
}

func TestDeterministicTieBreak(t *testing.T) {
	p := NewColorPolicy("mat-c5")
	mk := func(raw string) ColorDesc {
		return ColorDesc{Primaries: 1, Transfer: 1, Matrix: 1, Source: SourceContainer, Raw: raw}
	}
	d1, _ := p.Reconcile("v", []ColorDesc{mk("b"), mk("a")})
	d2, _ := p.Reconcile("v", []ColorDesc{mk("a"), mk("b")})
	if d1.Selected.Raw != "a" || d2.Selected.Raw != "a" {
		t.Fatalf("tie-break must be deterministic: %s vs %s", d1.Selected.Raw, d2.Selected.Raw)
	}
}

func TestDefaultLowestPriority(t *testing.T) {
	real := ColorDesc{Primaries: 9, Source: SourceContainer, Raw: "colr"}
	merged := MergeDefaults([]ColorDesc{real}, ColorDesc{Primaries: 1, Raw: "fallback"})
	dec, err := NewColorPolicy("mat-c6").Reconcile("v", merged)
	// Differing values are flagged as a (recoverable) conflict, but priority
	// must still select the real declaration over the low-priority default.
	if err != nil && !model.HasCode(err, model.CodeColorConflict) {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Selected.Source != SourceContainer || dec.Selected.Primaries != 9 {
		t.Fatal("real declaration must beat the low-priority default")
	}
}
