package media

import (
	"errors"
	"testing"
)

func TestChannelMappingRequiresExplicitMatrix(t *testing.T) {
	// No standard policy exists for 5.1 -> mono: must be rejected, not guessed.
	if _, err := StandardMatrix(Layout51, LayoutMono); !errors.Is(err, ErrLayoutMismatch) {
		t.Fatalf("expected ErrLayoutMismatch, got %v", err)
	}
	// Stereo -> mono is a documented fold.
	m, err := StandardMatrix(LayoutStereo, LayoutMono)
	if err != nil {
		t.Fatal(err)
	}
	out, err := m.ApplyPCM([]float64{0.4, 0.2})
	if err != nil || len(out) != 1 || abs(out[0]-0.3) > 1e-12 {
		t.Fatalf("stereo fold = %v err=%v", out, err)
	}
	// A matrix with an unmapped target row must fail validation.
	bad := MappingMatrix{From: LayoutStereo, To: LayoutStereo, Values: [][]float64{{1, 0}, {0, 0}}}
	if err := bad.Validate(); !errors.Is(err, ErrLayoutMismatch) {
		t.Fatalf("unmapped target row: %v", err)
	}
}

func Test51DownmixDropsLFE(t *testing.T) {
	m, err := StandardMatrix(Layout51, LayoutStereo)
	if err != nil {
		t.Fatal(err)
	}
	// L=1 R=2 C=3 LFE=99 Ls=5 Rs=6; LFE column must contribute nothing.
	in := []float64{1, 2, 3, 99, 5, 6}
	out, err := m.ApplyPCM(in)
	if err != nil {
		t.Fatal(err)
	}
	wantL := 1.0 + 0.7071*3 + 0.7071*5
	wantR := 2.0 + 0.7071*3 + 0.7071*6
	if abs(out[0]-wantL) > 1e-9 || abs(out[1]-wantR) > 1e-9 {
		t.Fatalf("downmix = %v want [%v %v]", out, wantL, wantR)
	}
}

func TestResampleDeterministicAndCounts(t *testing.T) {
	cfg := ResampleConfig{InputRate: 48000, OutputRate: 24000}
	// 480 stereo frames (1 second at 48k -> half a second at 24k).
	in := make([]float64, 480*2)
	for i := range in {
		in[i] = float64(i)
	}
	out1, rep, err := ResamplePCM(in, 2, cfg)
	if err != nil {
		t.Fatal(err)
	}
	out2, _, _ := ResamplePCM(in, 2, cfg)
	if rep.InputSamples != 480 || rep.OutputSamples != 240 {
		t.Fatalf("report = %+v", rep)
	}
	if len(out1) != 480 || len(out2) != len(out1) {
		t.Fatalf("output lengths %d %d", len(out1), len(out2))
	}
	for i := range out1 {
		if out1[i] != out2[i] {
			t.Fatalf("non-deterministic resample at %d", i)
		}
	}
	// Linear interpolation check: output frame 1 (slice index 2 for stereo)
	// sits halfway between input frames 2 and 3 on channel 0 (idx 4 and 6).
	// Output frame 1 maps to source position 2.0, so it equals input frame 2.
	if abs(out1[2]-in[4]) > 1e-9 {
		t.Fatalf("interp = %v want %v", out1[2], in[4])
	}
	// Output frame 2 maps to source position 4.0 => input frame 4 ch0 = idx 8.
	if abs(out1[4]-in[8]) > 1e-9 {
		t.Fatalf("interp frame2 = %v want %v", out1[4], in[8])
	}
	if err := (ResampleConfig{InputRate: 0, OutputRate: 24000}).Validate(); !errors.Is(err, ErrSampleRate) {
		t.Fatalf("zero rate: %v", err)
	}
}

func TestNormalizeAudioPlan(t *testing.T) {
	plan := AudioPlan{
		FromLayout: LayoutStereo, ToLayout: LayoutMono,
		Matrix:   mustMatrix(t, LayoutStereo, LayoutMono),
		Resample: ResampleConfig{InputRate: 48000, OutputRate: 48000},
	}
	in := []float64{0.2, 0.4, 0.6, 0.8}
	out, rep, err := NormalizeAudio(in, plan)
	if err != nil {
		t.Fatal(err)
	}
	if rep.InputSamples != 2 || rep.OutputSamples != 2 || len(out) != 2 {
		t.Fatalf("normalize = %v rep=%+v", out, rep)
	}
	// Tamper with the plan layout: matrix no longer matches, must reject.
	plan.ToLayout = LayoutStereo
	if _, _, err := NormalizeAudio(in, plan); !errors.Is(err, ErrLayoutMismatch) {
		t.Fatalf("plan mismatch: %v", err)
	}
}

func TestColorInheritanceAndConflict(t *testing.T) {
	pol := ProjectPolicy{Default: ColorMetadata{CPBT709, CPBT709, 1, true, ""}, AllowInheritance: true}
	tab := NewColorTable(pol)
	if err := tab.Ingest(ColorMetadata{SourceTrackID: "cam-a", Primaries: CPBT709, Transfer: CPBT709, Matrix: 1, FullRange: true}); err != nil {
		t.Fatal(err)
	}
	// Missing fields inherit project defaults.
	if err := tab.Ingest(ColorMetadata{SourceTrackID: "cam-b", Primaries: CPBT709, Transfer: CPBT709, Matrix: 1, FullRange: true}); err != nil {
		t.Fatal(err)
	}
	snap := tab.Snapshot()
	if snap["cam-b"].Primaries != CPBT709 {
		t.Fatalf("inheritance failed: %+v", snap["cam-b"])
	}
	// Explicit conflicting primaries must block and retain provenance.
	err := tab.Ingest(ColorMetadata{SourceTrackID: "cam-c", Primaries: CPBT2020, Transfer: CPBT709, Matrix: 1, FullRange: true})
	if !errors.Is(err, ErrColorConflict) {
		t.Fatalf("conflict expected, got %v", err)
	}
	if !tab.Blocked() || tab.Conflict() == nil || tab.Conflict().Field != "primaries" {
		t.Fatalf("conflict record = %+v", tab.Conflict())
	}
	if tab.Conflict().Incoming.Primaries != CPBT2020 || tab.Conflict().Existing.Primaries != CPBT709 {
		t.Fatal("source values must be retained on conflict")
	}
}

func TestColorInheritanceForbidden(t *testing.T) {
	pol := ProjectPolicy{Default: ColorMetadata{CPBT709, CPBT709, 1, true, ""}, AllowInheritance: false}
	tab := NewColorTable(pol)
	err := tab.Ingest(ColorMetadata{SourceTrackID: "cam-x"})
	if !errors.Is(err, ErrColorConflict) {
		t.Fatalf("inheritance must be blocked by policy: %v", err)
	}
}

func mustMatrix(t *testing.T, from, to Layout) MappingMatrix {
	t.Helper()
	m, err := StandardMatrix(from, to)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
