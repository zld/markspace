package track

import (
	"testing"

	"mediaregress/internal/media"
)

func sampleTrack(id string, kind Kind) *Track {
	return &Track{ID: id, Kind: kind, Timescale: 1000}
}

func TestBuildTrackLanesOrdersDependencies(t *testing.T) {
	g, err := BuildTrackLanes([]*Track{sampleTrack("v0", KindVideo), sampleTrack("a0", KindAudio)})
	if err != nil {
		t.Fatal(err)
	}
	// Timeline must not run before parse.
	if err := g.Ready(Item{"v0", NodeTimeline}); err == nil {
		t.Fatal("timeline ran before parse completed")
	}
	if err := g.Begin(Item{"v0", NodeParse}); err != nil {
		t.Fatal(err)
	}
	if err := g.Complete(Item{"v0", NodeParse}); err != nil {
		t.Fatal(err)
	}
	if err := g.Ready(Item{"v0", NodeTimeline}); err != nil {
		t.Fatalf("timeline should be ready: %v", err)
	}
	// Split still blocked by timeline.
	if err := g.Ready(Item{"v0", NodeSplit}); err == nil {
		t.Fatal("split ran before timeline")
	}
	if got := g.State(Item{"v0", NodeSilence}); got != StateSkipped {
		t.Fatalf("video silence state=%s want skipped", got)
	}
	// Complete timeline, then split; audio fingerprint must also wait on silence.
	for _, it := range []Item{{"v0", NodeTimeline}, {"v0", NodeSplit}} {
		if err := g.Begin(it); err != nil {
			t.Fatalf("begin %s: %v", it, err)
		}
		if err := g.Complete(it); err != nil {
			t.Fatalf("complete %s: %v", it, err)
		}
	}
	if err := g.Ready(Item{"v0", NodeFingerprint}); err != nil {
		t.Fatal(err)
	}
}

func TestAudioFingerprintWaitsForSilence(t *testing.T) {
	g, err := BuildTrackLanes([]*Track{sampleTrack("a0", KindAudio)})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []Node{NodeParse, NodeTimeline, NodeSplit, NodeSilence} {
		it := Item{"a0", n}
		if err := g.Begin(it); err != nil {
			t.Fatalf("begin %s: %v", it, err)
		}
		if err := g.Complete(it); err != nil {
			t.Fatalf("complete %s: %v", it, err)
		}
	}
	if err := g.Ready(Item{"a0", NodeFingerprint}); err != nil {
		t.Fatal(err)
	}
}

func TestFailureSkipsDependents(t *testing.T) {
	g, err := BuildTrackLanes([]*Track{sampleTrack("v0", KindVideo)})
	if err != nil {
		t.Fatal(err)
	}
	g.Fail(Item{"v0", NodeParse}, media.NewError(media.ClassCorruptInput, media.StageParse, "v0", 10, "bad"))
	for _, n := range []Node{NodeTimeline, NodeSplit, NodeFingerprint} {
		if got := g.State(Item{"v0", n}); got != StateSkipped {
			t.Errorf("%s state=%s want skipped", n, got)
		}
	}
	if err := g.Ready(Item{"v0", NodeTimeline}); err == nil {
		t.Fatal("dependent of failed node must not be ready")
	}
}

func TestRejectedTrackSkipsLane(t *testing.T) {
	tr := sampleTrack("v0", KindVideo)
	tr.Rejected = true
	g, err := BuildTrackLanes([]*Track{tr})
	if err != nil {
		t.Fatal(err)
	}
	if got := g.State(Item{"v0", NodeTimeline}); got != StateSkipped {
		t.Fatalf("rejected lane state=%s", got)
	}
}

func TestBuildTrackLanesEmpty(t *testing.T) {
	_, err := BuildTrackLanes(nil)
	if !media.IsClass(err, media.ClassDependency) {
		t.Fatalf("err=%v want dependency_incomplete", err)
	}
}

func TestAddNodeUnknownDependency(t *testing.T) {
	g := NewGraph()
	err := g.AddNode(Item{"v0", NodeSplit}, media.StageSplit, Item{"v0", NodeTimeline})
	if err == nil {
		t.Fatal("missing dependency accepted")
	}
}

func TestPendingTracks(t *testing.T) {
	g, err := BuildTrackLanes([]*Track{sampleTrack("a0", KindAudio)})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Begin(Item{"a0", NodeParse}); err != nil {
		t.Fatal(err)
	}
	if err := g.Complete(Item{"a0", NodeParse}); err != nil {
		t.Fatal(err)
	}
	pending := g.PendingTracks()
	got := pending["a0"]
	want := []Node{NodeTimeline, NodeSplit, NodeSilence, NodeFingerprint}
	if len(got) != len(want) {
		t.Fatalf("pending=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pending=%v want %v", got, want)
		}
	}
}

func TestApertureValidate(t *testing.T) {
	ok := Aperture{Left: 8, Top: 0, Width: 1904, Height: 1080}
	if err := ok.Validate(1920, 1080); err != nil {
		t.Fatalf("valid crop rejected: %v", err)
	}
	bad := []Aperture{
		{Width: 0, Height: 10},
		{Left: -1, Width: 10, Height: 10},
		{Left: 1910, Width: 20, Height: 10},
		{Top: 1070, Width: 10, Height: 20},
	}
	for i, a := range bad {
		if err := a.Validate(1920, 1080); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

func TestTrackSampleAt(t *testing.T) {
	tr := &Track{Samples: []Sample{
		{Index: 0, Start: 0, Duration: 100},
		{Index: 1, Start: 100, Duration: 100},
	}}
	if s, ok := tr.SampleAt(100); !ok || s.Index != 1 {
		t.Fatalf("boundary sample=%+v ok=%v", s, ok)
	}
	if _, ok := tr.SampleAt(200); ok {
		t.Fatal("end is exclusive")
	}
}

func TestDurationOrEnd(t *testing.T) {
	tr := &Track{Duration: 50, Samples: []Sample{{Start: 100, Duration: 10}}}
	if got := tr.DurationOrEnd(); got != 110 {
		t.Fatalf("DurationOrEnd=%d want 110", got)
	}
}

func TestValidTracksSorted(t *testing.T) {
	in := &Input{Tracks: []*Track{
		{ID: "v0"}, {ID: "a0", Rejected: true}, {ID: "a1"},
	}}
	got := in.ValidTracks()
	if len(got) != 2 || got[0].ID != "a1" || got[1].ID != "v0" {
		t.Fatalf("ValidTracks=%v", got)
	}
}
