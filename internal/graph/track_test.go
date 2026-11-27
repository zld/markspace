package graph

import (
	"reflect"
	"strings"
	"testing"

	"replay/internal/fault"
)

func track(id string, rate uint32, deps ...string) Track {
	return Track{ID: id, ClockRate: rate, DependsOn: deps}
}

func TestTopologicalOrderIsStableAndDependencyFirst(t *testing.T) {
	// Input order is deliberately scrambled.
	tracks := []Track{
		track("video", 90000, "audio"),
		track("mix", 48000, "audio", "video"),
		track("audio", 48000),
	}
	g, err := New(tracks)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := g.TopologicalOrder()
	if err != nil {
		t.Fatalf("TopologicalOrder: %v", err)
	}
	want := []string{"audio", "video", "mix"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order=%v want %v", got, want)
	}
	// Reordering the input must not change the plan.
	shuffled := []Track{tracks[2], tracks[0], tracks[1]}
	g2, _ := New(shuffled)
	got2, _ := g2.TopologicalOrder()
	if !reflect.DeepEqual(got2, want) {
		t.Fatalf("order changed with input order: %v", got2)
	}
}

func TestTopologicalTiesBreakByIdentifier(t *testing.T) {
	g, err := New([]Track{
		track("zeta", 48000),
		track("alpha", 48000),
		track("mid", 48000),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, _ := g.TopologicalOrder()
	want := []string{"alpha", "mid", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order=%v want %v", got, want)
	}
}

func TestCycleRejectedWithPath(t *testing.T) {
	_, err := New([]Track{
		track("a", 48000, "c"),
		track("b", 48000, "a"),
		track("c", 48000, "b"),
	})
	if err == nil {
		t.Fatal("expected cycle error")
	}
	fe, ok := fault.As(err)
	if !ok || fe.Kind != fault.KindGraph {
		t.Fatalf("kind: %v", err)
	}
	if !strings.Contains(fe.Reason, "a -> c -> b -> a") {
		t.Fatalf("reason=%q", fe.Reason)
	}
}

func TestSelfDependencyRejected(t *testing.T) {
	_, err := New([]Track{track("a", 48000, "a")})
	if err == nil || !fault.IsKind(err, fault.KindGraph) {
		t.Fatalf("want graph error, got %v", err)
	}
}

func TestUnknownDependencyRejected(t *testing.T) {
	_, err := New([]Track{track("a", 48000, "missing")})
	if err == nil || !fault.IsKind(err, fault.KindGraph) {
		t.Fatalf("want graph error, got %v", err)
	}
	if fe, _ := fault.As(err); fe.Track != "a" {
		t.Fatalf("track=%q", fe.Track)
	}
}

func TestDuplicateIDRejected(t *testing.T) {
	_, err := New([]Track{track("a", 48000), track("a", 48000)})
	if err == nil || !fault.IsKind(err, fault.KindGraph) {
		t.Fatalf("want graph error, got %v", err)
	}
}

func TestBadClockRateRejected(t *testing.T) {
	_, err := New([]Track{{ID: "a"}})
	if err == nil || !fault.IsKind(err, fault.KindGraph) {
		t.Fatalf("want graph error, got %v", err)
	}
}

func TestDownstreamIncludesSelfAndDependents(t *testing.T) {
	g, err := New([]Track{
		track("audio", 48000),
		track("video", 90000, "audio"),
		track("mix", 48000, "audio", "video"),
		track("unrelated", 48000),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := g.Downstream("video")
	want := []string{"mix", "video"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("downstream=%v want %v", got, want)
	}
	if got := g.Downstream("mix"); !reflect.DeepEqual(got, []string{"mix"}) {
		t.Fatalf("leaf downstream=%v", got)
	}
	if got := g.Downstream("missing"); got != nil {
		t.Fatalf("unknown downstream=%v", got)
	}
}

func TestTrackReturnsCopy(t *testing.T) {
	g, _ := New([]Track{track("a", 48000, "b"), track("b", 48000)})
	got, ok := g.Track("a")
	if !ok {
		t.Fatal("track a missing")
	}
	got.DependsOn[0] = "mutated"
	again, _ := g.Track("a")
	if again.DependsOn[0] != "b" {
		t.Fatalf("graph state mutated: %v", again.DependsOn)
	}
}
