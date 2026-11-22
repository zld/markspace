package graph

import (
	"strings"
	"testing"

	"mediacore/internal/core"
)

func TestOrderIsStableAndRespectsDependencies(t *testing.T) {
	g, err := Build(Plan{HaveAudioTrack: true, SampleRate: 48000, Channels: "stereo", TargetKbps: 128, LoudnessLUFS: -16, PeakDBFS: -1})
	if err != nil {
		t.Fatal(err)
	}
	first := g.Order()
	if len(first) != 7 {
		t.Fatalf("order size = %d", len(first))
	}
	for i := 0; i < 10; i++ {
		again, err := Build(Plan{HaveAudioTrack: true, SampleRate: 48000, Channels: "stereo", TargetKbps: 128, LoudnessLUFS: -16, PeakDBFS: -1})
		if err != nil {
			t.Fatal(err)
		}
		second := again.Order()
		for k := range first {
			if first[k] != second[k] {
				t.Fatalf("order differs at %d: %s vs %s", k, first[k], second[k])
			}
		}
	}
	pos := make(map[NodeID]int)
	for i, id := range first {
		pos[id] = i
	}
	for _, id := range first {
		n, _ := g.Node(id)
		for _, dep := range n.Deps {
			if pos[dep] >= pos[id] {
				t.Fatalf("%s comes before its dependency %s", id, dep)
			}
		}
	}
	if first[0] != NodeAudioTrack {
		t.Fatalf("expected audio-track first, got %s", first[0])
	}
	if first[len(first)-1] != NodePacketize {
		t.Fatalf("expected rtp-packetize last, got %s", first[len(first)-1])
	}
}

func TestCycleNamesConcreteNodes(t *testing.T) {
	_, err := New([]Node{
		{ID: "a", Deps: []NodeID{"b"}},
		{ID: "b", Deps: []NodeID{"c"}},
		{ID: "c", Deps: []NodeID{"a"}},
		{ID: "d"},
	})
	if !core.IsCode(err, core.CodeDependencyCycle) {
		t.Fatalf("want dependency_cycle, got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"a -> b -> c -> a"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not contain cycle path %q", msg, want)
		}
	}
	ce, _ := core.As(err)
	if ce.Node == "" {
		t.Fatal("cycle error has no node location")
	}
	if core.ExitCode(err) != 2 {
		t.Fatalf("cycle exit code = %d, want 2", core.ExitCode(err))
	}
}

func TestSelfCycleIsDetected(t *testing.T) {
	_, err := New([]Node{{ID: "loop", Deps: []NodeID{"loop"}}})
	if !core.IsCode(err, core.CodeDependencyCycle) {
		t.Fatalf("want dependency_cycle, got %v", err)
	}
	if !strings.Contains(err.Error(), "loop -> loop") {
		t.Fatalf("self cycle not named: %v", err)
	}
}

func TestMissingDependencyNamesNode(t *testing.T) {
	_, err := New([]Node{{ID: "resample", Deps: []NodeID{"timeline"}}})
	if !core.IsCode(err, core.CodeMissingTrack) {
		t.Fatalf("want missing_track, got %v", err)
	}
	ce, _ := core.As(err)
	if ce.Node != "resample" {
		t.Fatalf("error node = %q", ce.Node)
	}
	if !strings.Contains(err.Error(), "timeline") {
		t.Fatalf("missing dependency not named: %v", err)
	}
	if core.ExitCode(err) != 3 {
		t.Fatalf("missing-node exit code = %d, want 3", core.ExitCode(err))
	}
}

func TestMissingAudioTrackRejectedBeforeExecution(t *testing.T) {
	_, err := Build(Plan{HaveAudioTrack: false, SampleRate: 48000, Channels: "stereo", TargetKbps: 128})
	if !core.IsCode(err, core.CodeMissingTrack) {
		t.Fatalf("want missing_track, got %v", err)
	}
	ce, _ := core.As(err)
	if ce.Node != string(NodeAudioTrack) {
		t.Fatalf("node = %q", ce.Node)
	}
}

func TestParamConflictsNameBothNodes(t *testing.T) {
	_, err := New([]Node{
		{ID: "a", Params: map[string]string{"channels": "stereo"}},
		{ID: "b", Params: map[string]string{"channels": "mono"}},
	})
	if !core.IsCode(err, core.CodeParamConflict) {
		t.Fatalf("want parameter_conflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "channels") || !strings.Contains(err.Error(), "stereo") || !strings.Contains(err.Error(), "mono") {
		t.Fatalf("conflict detail missing: %v", err)
	}
}

func TestPlanRejectsNonPositiveSettings(t *testing.T) {
	cases := []Plan{
		{HaveAudioTrack: true, SampleRate: 0, Channels: "stereo", TargetKbps: 128},
		{HaveAudioTrack: true, SampleRate: 48000, Channels: "", TargetKbps: 128},
		{HaveAudioTrack: true, SampleRate: 48000, Channels: "stereo", TargetKbps: 0},
		{HaveAudioTrack: true, SampleRate: 48000, Channels: "stereo", TargetKbps: 128, PeakDBFS: 3},
	}
	for i, p := range cases {
		if _, err := Build(p); !core.IsCode(err, core.CodeParamConflict) {
			t.Fatalf("case %d: want parameter_conflict, got %v", i, err)
		}
	}
}
