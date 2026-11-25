package track

import (
	"errors"
	"testing"

	"rtptrace/internal/manifest"
)

func tracks(ids ...uint32) []manifest.Track {
	out := make([]manifest.Track, 0, len(ids))
	for _, id := range ids {
		out = append(out, manifest.Track{ID: id})
	}
	return out
}

func TestGraphOrderIsDependencyThenID(t *testing.T) {
	g, err := NewGraph(tracks(3, 1, 2), []manifest.Dependency{
		{TrackID: 3, DependsOn: 2},
		{TrackID: 2, DependsOn: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []uint32{1, 2, 3}
	if len(g.Order) != 3 || g.Order[0] != 1 || g.Order[1] != 2 || g.Order[2] != 3 {
		t.Fatalf("order = %v, want %v", g.Order, want)
	}
}

func TestGraphReportsCycleIDsAndEdges(t *testing.T) {
	_, err := NewGraph(tracks(1, 2, 3), []manifest.Dependency{
		{TrackID: 1, DependsOn: 2},
		{TrackID: 2, DependsOn: 1},
		{TrackID: 3, DependsOn: 1},
	})
	var cycle *CycleError
	if !errors.As(err, &cycle) {
		t.Fatalf("expected cycle, got %v", err)
	}
	if len(cycle.TrackIDs) != 2 || cycle.TrackIDs[0] != 1 || cycle.TrackIDs[1] != 2 {
		t.Fatalf("cycle ids = %v", cycle.TrackIDs)
	}
	if len(cycle.Dependencies) != 2 {
		t.Fatalf("cycle edges = %+v", cycle.Dependencies)
	}
}

func TestInvalidDependencyNamesEdge(t *testing.T) {
	_, err := NewGraph(tracks(1), []manifest.Dependency{{TrackID: 1, DependsOn: 9}})
	var invalid *InvalidDependencyError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected invalid dependency, got %v", err)
	}
	if invalid.Dependency.DependsOn != 9 {
		t.Fatalf("bad edge: %+v", invalid.Dependency)
	}
}

func TestFailedDependencyBlocksDownstreamPublish(t *testing.T) {
	g, err := NewGraph(tracks(1, 2, 3), []manifest.Dependency{
		{TrackID: 2, DependsOn: 1},
		{TrackID: 3, DependsOn: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	ex := g.Run(func(id uint32) error {
		if id == 1 {
			return errors.New("normalize failed")
		}
		return nil
	})
	if ex.States[1].State != StateFailed {
		t.Fatalf("track1 = %s", ex.States[1].State)
	}
	if ex.States[2].State != StateBlocked || ex.States[3].State != StateBlocked {
		t.Fatalf("propagation = %s/%s", ex.States[2].State, ex.States[3].State)
	}
	if ex.PublishAllowed() {
		t.Fatal("publish must not be allowed")
	}
}
