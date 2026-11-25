package tracks_test

import (
	"testing"

	"mediacheck/internal/ebml"
	"mediacheck/internal/report"
	"mediacheck/internal/sample"
	"mediacheck/internal/tracks"
)

func buildGraph(t *testing.T, caseName string) *tracks.Graph {
	t.Helper()
	b, err := sample.Generate(caseName, 7)
	if err != nil {
		t.Fatal(err)
	}
	doc, serr := ebml.ParseDocument(b.EBML)
	if serr != nil {
		t.Fatalf("parse: %v", serr)
	}
	g, gerr := tracks.BuildGraph(b.EBML, doc)
	if gerr != nil {
		t.Fatalf("BuildGraph: %v", gerr)
	}
	return g
}

func TestNormalTrackGraphHasNoConflicts(t *testing.T) {
	g := buildGraph(t, sample.CaseNormal)
	issues := g.Validate()
	if len(issues) != 0 {
		t.Fatalf("expected no track issues, got %+v", issues)
	}
	if len(g.Tracks) != 2 {
		t.Fatalf("tracks=%d want 2", len(g.Tracks))
	}
}

func TestAudioConflictReportsLayoutSampleRateAndOffsets(t *testing.T) {
	g := buildGraph(t, sample.CaseAudioConflict)
	issues := g.Validate()

	var layout, rate *report.Record
	for i := range issues {
		switch issues[i].ErrorCode {
		case report.TrackLayoutConflict:
			layout = &issues[i]
		case report.TrackSampleRateConflict:
			rate = &issues[i]
		}
	}
	if layout == nil || rate == nil {
		t.Fatalf("missing expected conflicts: %+v", issues)
	}
	if layout.Track != 2 || layout.InputOffset != 158 || layout.FailureOffset != 158 {
		t.Fatalf("layout location unstable: %+v", layout)
	}
	if layout.ByteDelta != -4 {
		t.Fatalf("layout byte/channel delta=%d want -4", layout.ByteDelta)
	}
	if rate.Track != 2 || rate.InputOffset != 162 || rate.FailureOffset != 162 {
		t.Fatalf("sample rate location unstable: %+v", rate)
	}
	if got := g.Order; len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("dependency validation order=%v want [1 2]", got)
	}
}

func TestDependencyTrackIsValidatedFirst(t *testing.T) {
	g := &tracks.Graph{Tracks: map[uint64]*tracks.Track{
		1: {Number: 1, Type: tracks.LayoutStereo, DependsOn: []uint64{2}},
		2: {Number: 2, Type: tracks.LayoutStereo},
	}}
	_ = g.Validate()
	if len(g.Order) != 2 || g.Order[0] != 2 || g.Order[1] != 1 {
		t.Fatalf("order=%v want dependency 2 before 1", g.Order)
	}
}

func TestMissingDependencyIsStructuredIssue(t *testing.T) {
	g := &tracks.Graph{Tracks: map[uint64]*tracks.Track{
		1: {Number: 1, DependsOn: []uint64{42}, DependencyOff: []int{123}},
	}}
	issues := g.Validate()
	if len(issues) != 1 || issues[0].ErrorCode != report.TrackDependency {
		t.Fatalf("issues=%+v want one dependency issue", issues)
	}
	if issues[0].InputOffset != 123 || issues[0].Track != 1 {
		t.Fatalf("dependency issue location wrong: %+v", issues[0])
	}
}

func TestDependencyCycleDoesNotLoop(t *testing.T) {
	g := &tracks.Graph{Tracks: map[uint64]*tracks.Track{
		1: {Number: 1, DependsOn: []uint64{2}},
		2: {Number: 2, DependsOn: []uint64{1}},
	}}
	issues := g.Validate()
	var cycle bool
	for _, is := range issues {
		if is.Category == "dependency_cycle" {
			cycle = true
		}
	}
	if !cycle {
		t.Fatalf("expected cycle issue, got %+v", issues)
	}
}

func TestColorMetadataConflictPointsAtBadField(t *testing.T) {
	g := &tracks.Graph{Tracks: map[uint64]*tracks.Track{
		1: {
			Number: 1,
			Type:   ebml.TrackTypeVideo,
			Color: &tracks.ColorMetadata{
				Matrix: 1, MatrixOffset: 100,
				Transfer: 1, TransferOffset: 104,
				Primaries: 6, PrimariesOffset: 108,
			},
		},
	}}
	issues := g.Validate()
	if len(issues) != 1 {
		t.Fatalf("issues=%+v want one color conflict", issues)
	}
	if issues[0].ErrorCode != report.TrackColorConflict || issues[0].InputOffset != 108 {
		t.Fatalf("color issue=%+v want offset 108", issues[0])
	}
}
