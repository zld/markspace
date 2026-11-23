package track

import (
	"errors"
	"strings"
	"testing"

	"regional.example/mediad/internal/fault"
	"regional.example/mediad/internal/manifest"
)

func audioAsset() *manifest.Asset {
	return &manifest.Asset{
		Version: 1, AssetID: "g", MovieTimescale: 48000,
		Tracks: []manifest.TrackDecl{{
			ID: 1, Role: manifest.RoleAudio, Codec: manifest.CodecPCM16, Required: true,
			Path: "a.mp4", SampleRate: 48000, Channels: 2, Layout: "stereo", MediaTimescale: 48000,
		}},
	}
}

func videoDecl(id uint32, required bool, deps ...uint32) manifest.TrackDecl {
	return manifest.TrackDecl{ID: id, Role: manifest.RoleVideo, Codec: "avc1", Required: required, Path: "v.mp4", DependsOn: deps}
}

func TestBuildLinearOrder(t *testing.T) {
	a := audioAsset()
	a.Tracks = append(a.Tracks, videoDecl(3, false, 2))
	a.Tracks = append(a.Tracks, videoDecl(2, false, 1))
	g, err := Build(a)
	if err != nil {
		t.Fatal(err)
	}
	order := g.Order()
	want := []uint32{1, 2, 3}
	if len(order) != 3 {
		t.Fatalf("order %v", order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order %v, want %v", order, want)
		}
	}
}

func TestBuildDetectsCycle(t *testing.T) {
	a := audioAsset()
	a.Tracks = append(a.Tracks, videoDecl(2, false, 3))
	a.Tracks = append(a.Tracks, videoDecl(3, false, 2))
	_, err := Build(a)
	if !errors.Is(err, fault.OfClass(fault.ClassDependency, "cycle")) {
		t.Fatalf("got %v", err)
	}
}

func TestBuildDetectsSelfCycle(t *testing.T) {
	a := audioAsset()
	a.Tracks[0].DependsOn = []uint32{1}
	_, err := Build(a)
	if !errors.Is(err, fault.OfClass(fault.ClassDependency, "self-edge")) {
		t.Fatalf("got %v", err)
	}
}

func TestBuildMissingOptionalDegrades(t *testing.T) {
	a := audioAsset()
	a.Tracks = append(a.Tracks, videoDecl(2, false, 9)) // dependency 9 absent
	g, err := Build(a)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := g.NodeByID(2)
	if n.State != StateDegraded {
		t.Fatalf("state %s", n.State)
	}
	if !strings.Contains(n.Reason, "not declared") {
		t.Fatalf("reason %q", n.Reason)
	}
}

func TestBuildMissingRequiredDepFails(t *testing.T) {
	a := audioAsset()
	a.Tracks = append(a.Tracks, videoDecl(2, true, 9))
	_, err := Build(a)
	if !errors.Is(err, fault.OfClass(fault.ClassDependency, "missing-required-dep")) {
		t.Fatalf("got %v", err)
	}
}

func TestBuildRequiredAudioUnsupportedFails(t *testing.T) {
	a := audioAsset()
	a.Tracks[0].Codec = "aac"
	_, err := Build(a)
	if !errors.Is(err, fault.OfClass(fault.ClassManifest, "codec-unsupported")) {
		t.Fatalf("got %v", err)
	}
}

func TestBuildOptionalAudioUnsupportedSkipped(t *testing.T) {
	a := audioAsset()
	opt := a.Tracks[0]
	opt.ID = 2
	opt.Required = false
	opt.Codec = "aac"
	opt.Path = "b.mp4"
	a.Tracks = append(a.Tracks, opt)
	g, err := Build(a)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := g.NodeByID(2)
	if n.State != StateSkipped {
		t.Fatalf("state %s", n.State)
	}
	if len(g.Skipped()) != 1 {
		t.Fatalf("skipped %v", g.Skipped())
	}
	if len(g.Order()) != 1 || g.Order()[0] != 1 {
		t.Fatalf("order %v must exclude skipped track", g.Order())
	}
}

func TestBuildDependentOnSkippedRequiredFails(t *testing.T) {
	a := audioAsset()
	opt := a.Tracks[0]
	opt.ID = 2
	opt.Required = false
	opt.Codec = "aac"
	opt.Path = "b.mp4"
	a.Tracks = append(a.Tracks, opt)
	a.Tracks = append(a.Tracks, videoDecl(3, true, 2))
	_, err := Build(a)
	if !errors.Is(err, fault.OfClass(fault.ClassDependency, "required-dep-skipped")) {
		t.Fatalf("got %v", err)
	}
}

func TestPlanOrder(t *testing.T) {
	a := audioAsset()
	g, err := Build(a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := BuildPlan(g)
	if err != nil {
		t.Fatal(err)
	}
	want := []StepKind{StepRepairIndex, StepReadSamples, StepResample, StepLayout, StepLoudness, StepRTPSummary, StepEmitManifest}
	if len(p.Steps) != len(want) {
		t.Fatalf("steps %v", p.Summary())
	}
	for i, k := range want {
		if p.Steps[i].Kind != k {
			t.Fatalf("step %d = %s, want %s", i, p.Steps[i].Kind, k)
		}
	}
}

func TestPlanDegradedNotesCarryReason(t *testing.T) {
	a := audioAsset()
	a.Tracks = append(a.Tracks, videoDecl(2, false, 9))
	g, err := Build(a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := BuildPlan(g)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Degraded) != 1 || p.Degraded[0].Decl.ID != 2 {
		t.Fatalf("degraded %v", p.Degraded)
	}
	if !strings.Contains(p.Degraded[0].Reason, "not declared") {
		t.Fatalf("reason lost: %q", p.Degraded[0].Reason)
	}
}

func TestPlanSkipsRecorded(t *testing.T) {
	a := audioAsset()
	opt := a.Tracks[0]
	opt.ID = 5
	opt.Required = false
	opt.Codec = "aac"
	opt.Path = "b.mp4"
	a.Tracks = append(a.Tracks, opt)
	g, _ := Build(a)
	p, err := BuildPlan(g)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Skipped) != 1 {
		t.Fatalf("skipped %v", p.Skipped)
	}
	if p.HasStep(StepResample, 5) {
		t.Fatal("skipped track must not have a pipeline")
	}
}

func TestPlanGlobalStepsRunOnce(t *testing.T) {
	a := audioAsset()
	a.Tracks = append(a.Tracks, videoDecl(2, false, 1))
	g, _ := Build(a)
	p, _ := BuildPlan(g)
	var rtp, emit int
	for _, s := range p.Steps {
		switch s.Kind {
		case StepRTPSummary:
			rtp++
		case StepEmitManifest:
			emit++
		}
	}
	if rtp != 1 || emit != 1 {
		t.Fatalf("global steps rtp=%d emit=%d", rtp, emit)
	}
}

func TestPlanStepsForTrack(t *testing.T) {
	a := audioAsset()
	g, _ := Build(a)
	p, _ := BuildPlan(g)
	if got := len(p.StepsFor(1)); got != 5 {
		t.Fatalf("steps for track 1: %d", got)
	}
}
