package timeline

import (
	"strings"
	"testing"

	"mediapack/merr"
)

func mustGraph(t *testing.T, tracks ...*Track) *Graph {
	t.Helper()
	g := NewGraph()
	for _, tr := range tracks {
		if err := g.Add(tr); err != nil {
			t.Fatalf("add %s: %v", tr.ID, err)
		}
	}
	return g
}

func TestGraphTopoOrderDeterministic(t *testing.T) {
	g := mustGraph(t,
		&Track{ID: "video-main", Kind: KindVideo},
		&Track{ID: "audio-main", Kind: KindAudio},
		&Track{ID: "remote-rtp", Kind: KindRTPVideo},
		&Track{ID: "program-out", Kind: KindOutput, DependsOn: []string{"video-main", "audio-main", "remote-rtp"}},
	)
	order, err := g.Order()
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	want := []string{"audio-main", "remote-rtp", "video-main", "program-out"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order=%v want %v", order, want)
	}
	// output must come after all its dependencies
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	for _, dep := range []string{"video-main", "audio-main", "remote-rtp"} {
		if pos[dep] > pos["program-out"] {
			t.Fatalf("dependency %s ordered after output", dep)
		}
	}
}

func TestGraphCycleRejected(t *testing.T) {
	g := mustGraph(t,
		&Track{ID: "a", DependsOn: []string{"b"}},
		&Track{ID: "b", DependsOn: []string{"a"}},
	)
	_, err := g.Order()
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if merr.KindOf(err) != merr.KindCorrupt {
		t.Fatalf("kind=%v want corrupt", merr.KindOf(err))
	}
	if !strings.Contains(err.Error(), "stage=graph") || !strings.Contains(err.Error(), "a") {
		t.Fatalf("error lacks stage/track context: %v", err)
	}
}

func TestGraphMissingDependencyRejected(t *testing.T) {
	g := mustGraph(t, &Track{ID: "out", DependsOn: []string{"ghost"}})
	if _, err := g.Order(); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("expected missing dependency error, got %v", err)
	}
}

func TestGraphDuplicateIDRejected(t *testing.T) {
	g := NewGraph()
	if err := g.Add(&Track{ID: "x"}); err != nil {
		t.Fatal(err)
	}
	err := g.Add(&Track{ID: "x"})
	if err == nil || !strings.Contains(err.Error(), "track=x") {
		t.Fatalf("expected duplicate ID error with track context, got %v", err)
	}
}

func TestClipHalfOpenAndValidation(t *testing.T) {
	c := Clip{TrackID: "v", SourceIn: 10, SourceOut: 20}
	if !c.Contains(10) || !c.Contains(19) || c.Contains(20) || c.Contains(9) {
		t.Fatal("half-open containment wrong")
	}
	if c.Duration() != 10 {
		t.Fatal("duration wrong")
	}
	for _, bad := range []Clip{
		{TrackID: "v", SourceIn: 5, SourceOut: 5},   // zero length
		{TrackID: "v", SourceIn: 6, SourceOut: 5},   // inverted
		{TrackID: "v", SourceIn: -1, SourceOut: 5},  // negative
		{TrackID: "v", SourceIn: 0, SourceOut: 5, TimelineStart: -1},
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("clip %+v should be rejected", bad)
		} else if !strings.Contains(err.Error(), "track=v") {
			t.Fatalf("clip error lacks track context: %v", err)
		}
	}
}

func TestTransitionValidationAndBlendOrder(t *testing.T) {
	g := mustGraph(t, &Track{ID: "a"}, &Track{ID: "b"})
	tr1 := Transition{ID: "t1", FromTrack: "a", ToTrack: "b", Start: 100, Duration: 50, Kind: "crossfade"}
	tr2 := Transition{ID: "t2", FromTrack: "b", ToTrack: "a", Start: 120, Duration: 50, Kind: "crossfade"}
	if err := ValidateTransitions([]Transition{tr1, tr2}, g); err != nil {
		t.Fatalf("valid transitions rejected: %v", err)
	}
	// overlapping region: both active, timeline order preserved
	act := ActiveTransitions([]Transition{tr2, tr1}, 130)
	if len(act) != 2 || act[0].ID != "t1" || act[1].ID != "t2" {
		t.Fatalf("active transitions not in timeline order: %+v", act)
	}
	if w := BlendWeight(125, tr1); w != 0.5 {
		t.Fatalf("blend weight=%v want 0.5", w)
	}
	if w := BlendWeight(200, tr1); w != 1 {
		t.Fatalf("blend weight after end=%v want 1", w)
	}
	// out-of-range reference
	bad := Transition{ID: "t3", FromTrack: "a", ToTrack: "ghost", Start: 0, Duration: 10, Kind: "crossfade"}
	if err := ValidateTransitions([]Transition{bad}, g); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("expected unknown track error, got %v", err)
	}
	// zero length
	zero := Transition{ID: "t4", FromTrack: "a", ToTrack: "b", Start: 0, Duration: 0, Kind: "crossfade"}
	if err := ValidateTransitions([]Transition{zero}, g); err == nil {
		t.Fatal("zero-length transition accepted")
	}
}

func TestClockWrapAndMonotonic(t *testing.T) {
	var se SeqExtender
	seqs := []uint16{65534, 65535, 0, 1}
	var ext []int32
	for _, s := range seqs {
		ext = append(ext, se.Extend(s))
	}
	for i := 1; i < len(ext); i++ {
		if ext[i] != ext[i-1]+1 {
			t.Fatalf("extended seq not contiguous: %v", ext)
		}
	}
	var te TsExtender
	ts0 := te.Extend(0xfffffff0)
	ts1 := te.Extend(0x00000010)
	if ts1-ts0 != 0x20 {
		t.Fatalf("timestamp wrap extension wrong: %d -> %d", ts0, ts1)
	}
	if err := CheckMonotonic("test", []int64{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := CheckMonotonic("test", []int64{1, 3, 3}); err == nil {
		t.Fatal("non-monotonic times accepted")
	}
	if got := TicksToNanos(3000, 90000); got != 33333333 {
		t.Fatalf("ticks to nanos=%d", got)
	}
	n, err := FrameCount(0, 4*NanosPerSecond, 30)
	if err != nil || n != 120 {
		t.Fatalf("frame count=%d err=%v", n, err)
	}
}
