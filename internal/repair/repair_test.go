package repair_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zld/continuity/internal/clock"
	"github.com/zld/continuity/internal/demux"
	"github.com/zld/continuity/internal/mux"
	"github.com/zld/continuity/internal/repair"
)

// customDeltas builds a video track whose stts deltas encode explicit
// timestamp defects: a backward jump and a duplicate run.
func defectTrack() mux.TrackSpec {
	deltas := []uint32{10, 10, 5, 0, 10, 10} // zero delta -> missing/duplicate
	specs := make([]mux.SampleSpec, len(deltas))
	for i, d := range deltas {
		specs[i] = mux.SampleSpec{Delta: d, Sync: i%2 == 0}
	}
	return mux.TrackSpec{Timescale: 25, Video: true, Samples: specs}
}

func open(t *testing.T, tracks []mux.TrackSpec) *demux.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "in.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mux.WriteFile(f, tracks, mux.FaultNone); err != nil {
		t.Fatal(err)
	}
	f.Close()
	d, err := demux.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRepairMissingAndDuplicate(t *testing.T) {
	d := open(t, []mux.TrackSpec{defectTrack()})
	defer d.Close()

	res, err := repair.Track(d, clock.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tracks) != 1 {
		t.Fatalf("tracks = %d", len(res.Tracks))
	}
	rt := res.Tracks[0]
	if err := repair.VerifyMonotonic(rt); err != nil {
		t.Fatalf("not monotonic: %v", err)
	}
	// Strictly increasing programme time for every row.
	for i := 1; i < len(rt.Samples); i++ {
		if rt.Samples[i].Value.Cmp(rt.Samples[i-1].Value) <= 0 {
			t.Fatalf("row %d did not advance", i)
		}
	}
	if res.RepairedCount() == 0 {
		t.Fatal("expected at least one repaired timestamp")
	}
	// stts-derived DTS cannot encode a negative delta, so the serialized
	// fixture yields missing/duplicate defects; genuine backward jumps are
	// covered by TestRepairBackwardJumpFromSourceSwitch.
	sawMissing := false
	for _, a := range res.Audit {
		if a.Reason == clock.ReasonMissing || a.Reason == clock.ReasonDuplicate {
			sawMissing = true
		}
		// The audit must point at the repaired value that actually landed in
		// the output row, and the reason must be a real defect class.
		if res.Tracks[0].Samples[a.Sample].Value.Cmp(a.Repaired) != 0 {
			t.Fatalf("audit %+v does not match output row", a)
		}
		if a.Reason == clock.ReasonNone {
			t.Fatalf("audit entry with no reason: %+v", a)
		}
	}
	if !sawMissing {
		t.Fatal("expected a missing/duplicate audit reason")
	}
}

func TestRepairPreservesLocations(t *testing.T) {
	d := open(t, []mux.TrackSpec{defectTrack()})
	defer d.Close()
	res, err := repair.Track(d, clock.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	rt := res.Tracks[0]
	for i, loc := range rt.Locations {
		src := d.Tracks[0].Samples[i]
		if loc.FileOffset != src.FileOffset || loc.Size != src.Size || loc.Sync != src.Sync {
			t.Fatalf("location %d drifted: %+v vs %+v", i, loc, src)
		}
	}
}

func TestRepairStrictRejects(t *testing.T) {
	d := open(t, []mux.TrackSpec{defectTrack()})
	defer d.Close()
	p := clock.DefaultPolicy()
	p.Mode = clock.ModeStrict
	_, err := repair.Track(d, p)
	if !errors.Is(err, clock.ErrCorrupt) {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
	var ce *clock.Error
	if !errors.As(err, &ce) || ce.Stage != clock.StageRepair || !ce.KnownT {
		t.Fatalf("error must identify repair stage and track: %v", err)
	}
}

func TestRepairCleanInputHasNoAudit(t *testing.T) {
	specs := make([]mux.SampleSpec, 8)
	for i := range specs {
		specs[i] = mux.SampleSpec{Delta: 1, Sync: i%4 == 0}
	}
	d := open(t, []mux.TrackSpec{{Timescale: 25, Video: true, Samples: specs}})
	defer d.Close()
	res, err := repair.Track(d, clock.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if res.RepairedCount() != 0 || len(res.Audit) != 0 {
		t.Fatalf("clean input produced audit entries: %d", len(res.Audit))
	}
	for i := range res.Tracks[0].Samples {
		want := clock.MustMake(int64(i), 25)
		if res.Tracks[0].Samples[i].Value.Cmp(want) != 0 {
			t.Fatalf("sample %d drifted to %s", i, res.Tracks[0].Samples[i].Value)
		}
	}
}

func TestRepairDoesNotMutateDemux(t *testing.T) {
	d := open(t, []mux.TrackSpec{defectTrack()})
	defer d.Close()
	before := d.Tracks[0].Samples[3].DTS
	if _, err := repair.Track(d, clock.DefaultPolicy()); err != nil {
		t.Fatal(err)
	}
	if d.Tracks[0].Samples[3].DTS.Cmp(before) != 0 {
		t.Fatal("demux sample table was mutated by repair")
	}
}
