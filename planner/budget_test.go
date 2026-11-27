package planner

import (
	"path/filepath"
	"testing"

	"continuity/fixture"
	"continuity/media"
)

func probeOne(t *testing.T, dir, name string, spec fixture.Spec) *media.Container {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := fixture.Generate(p, spec); err != nil {
		t.Fatal(err)
	}
	c, err := media.Probe(p)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func limits() Constraints {
	return Constraints{MaxBufferBytes: 1 << 20, MaxConcurrency: 4}
}

func TestEvaluateValid(t *testing.T) {
	dir := t.TempDir()
	in1 := probeOne(t, dir, "a.mp4", fixture.ValidSpec())
	in2 := probeOne(t, dir, "b.mp4", fixture.ValidSpec())
	p, rej, err := Evaluate([]*media.Container{in1, in2}, 2_000_000_000, limits())
	if err != nil {
		t.Fatal(err)
	}
	if len(rej) != 0 {
		t.Fatalf("unexpected rejections: %+v", rej)
	}
	// 6 s program at 2 s segments: cuts at 0, 2, 4, 6 s.
	if len(p.Cuts) != 4 || p.Cuts[1] != 2_000_000_000 {
		t.Fatalf("cuts %v", p.Cuts)
	}
	if len(p.Audio) != 2 || p.Audio[0].ToChannels != 2 {
		t.Fatalf("audio plan %+v", p.Audio)
	}
	if len(p.PlanHash) != 64 {
		t.Fatalf("plan hash %q", p.PlanHash)
	}
}

func TestEvaluateBudgetExhausted(t *testing.T) {
	dir := t.TempDir()
	in1 := probeOne(t, dir, "a.mp4", fixture.ValidSpec())
	c := limits()
	c.MaxBufferBytes = 1024 // far below any candidate
	_, rej, err := Evaluate([]*media.Container{in1}, 2_000_000_000, c)
	if !media.IsKind(err, media.KindBudget) {
		t.Fatalf("want budget error, got %v", err)
	}
	if len(rej) != 3 {
		t.Fatalf("want 3 rejected candidates, got %d", len(rej))
	}
	for _, r := range rej {
		if r.Reason == "" {
			t.Fatal("rejection missing reason")
		}
	}
}

func TestEvaluateAudioConflictNeedsTarget(t *testing.T) {
	dir := t.TempDir()
	in1 := probeOne(t, dir, "a.mp4", fixture.ValidSpec())
	in2 := probeOne(t, dir, "b.mp4", fixture.AudioConflictSpec())
	_, _, err := Evaluate([]*media.Container{in1, in2}, 2_000_000_000, limits())
	if !media.IsKind(err, media.KindParamConflict) {
		t.Fatalf("want param-conflict, got %v", err)
	}
}

func TestEvaluateAudioNoMappingRule(t *testing.T) {
	dir := t.TempDir()
	in1 := probeOne(t, dir, "a.mp4", fixture.AudioConflictSpec()) // 6 ch
	c := limits()
	c.TargetChannels = 4 // no 6 -> 4 rule
	c.TargetSampleRate = 48000
	_, _, err := Evaluate([]*media.Container{in1}, 2_000_000_000, c)
	if !media.IsKind(err, media.KindParamConflict) {
		t.Fatalf("want param-conflict, got %v", err)
	}
	me := err.(*media.Error)
	if me.TrackID != 2 {
		t.Fatalf("rejection must name track 2, got %d", me.TrackID)
	}
}

func TestEvaluateAudioDownmixPlanned(t *testing.T) {
	dir := t.TempDir()
	in1 := probeOne(t, dir, "a.mp4", fixture.AudioConflictSpec()) // 6 ch / 44.1k
	c := limits()
	c.TargetChannels = 2
	c.TargetSampleRate = 48000
	p, _, err := Evaluate([]*media.Container{in1}, 2_000_000_000, c)
	if err != nil {
		t.Fatal(err)
	}
	if p.Audio[0].FromChannels != 6 || p.Audio[0].ToChannels != 2 ||
		p.Audio[0].FromRate != 44100 || p.Audio[0].ToRate != 48000 {
		t.Fatalf("conversion %+v", p.Audio[0])
	}
}

func TestEvaluateColorConflictNeedsSelection(t *testing.T) {
	dir := t.TempDir()
	in1 := probeOne(t, dir, "a.mp4", fixture.ValidSpec())
	in2 := probeOne(t, dir, "b.mp4", fixture.ColorConflictSpec())
	_, _, err := Evaluate([]*media.Container{in1, in2}, 2_000_000_000, limits())
	if !media.IsKind(err, media.KindParamConflict) {
		t.Fatalf("want param-conflict, got %v", err)
	}
	// With an explicit output value the plan must keep per-rep sources.
	c := limits()
	out := media.ColorMeta{Present: true, Primaries: 1, Transfer: 1, Matrix: 1}
	c.ColorOutput = &out
	p, _, err := Evaluate([]*media.Container{in1, in2}, 2_000_000_000, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ColorSources) != 2 || p.ColorSources[1].Primaries != 9 {
		t.Fatalf("color sources %+v", p.ColorSources)
	}
	if p.ColorOutput.Primaries != 1 {
		t.Fatalf("color output %+v", p.ColorOutput)
	}
}
