package model

import (
	"errors"
	"testing"

	"archive/internal/fault"
)

func track(key string, required bool, deps ...string) *Track {
	return &Track{
		Key:       key,
		Container: ContainerMP4,
		TrackID:   1,
		Required:  required,
		Timebase:  Timebase{Timescale: 1000},
		DependsOn: deps,
		Fragments: []Fragment{{Seq: 0}, {Seq: 1}},
	}
}

func TestGraphTopoOrderResolvesDependenciesFirst(t *testing.T) {
	g, err := BuildGraph([]*Track{track("video", true), track("audio", true, "video")})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"video", "audio"}
	if len(g.TrackKeys) != len(want) {
		t.Fatalf("order = %v", g.TrackKeys)
	}
	for i := range want {
		if g.TrackKeys[i] != want[i] {
			t.Fatalf("order = %v, want %v", g.TrackKeys, want)
		}
	}
}

func TestGraphDetectsCycle(t *testing.T) {
	_, err := BuildGraph([]*Track{track("a", true, "b"), track("b", true, "a")})
	if err == nil {
		t.Fatal("expected cycle error")
	}
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Code != fault.DependencyCycle {
		t.Fatalf("error = %v", err)
	}
}

func TestGraphMissingRequiredTrack(t *testing.T) {
	_, err := BuildGraph([]*Track{track("audio", true, "video")})
	if err == nil {
		t.Fatal("expected missing required track error")
	}
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Code != fault.MissingRequiredTrack {
		t.Fatalf("error = %v", err)
	}
}

func TestGraphOptionalTrackMayBeAbsent(t *testing.T) {
	g, err := BuildGraph([]*Track{track("video", true), track("aux", false, "missing-data")})
	if err != nil {
		t.Fatalf("optional absence must not fail mapping: %v", err)
	}
	if len(g.Missing) != 1 || g.Missing[0] != "missing-data" {
		t.Fatalf("missing = %v", g.Missing)
	}
}

func TestGraphDuplicateNode(t *testing.T) {
	g := NewGraph()
	if err := g.Add(&Node{ID: "track:a"}); err != nil {
		t.Fatal(err)
	}
	if err := g.Add(&Node{ID: "track:a"}); err == nil {
		t.Fatal("expected duplicate node error")
	}
}
