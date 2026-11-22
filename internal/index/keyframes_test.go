package index

import (
	"testing"

	"github.com/pairwise/mediad/internal/fault"
	"github.com/pairwise/mediad/internal/mp4"
)

// linearPres treats dts as ns presentation time.
type linearPres struct{ movie *mp4.Movie }

func (l linearPres) At(track uint32, i int) (int64, bool) {
	for _, tr := range l.movie.Tracks {
		if tr.ID == track && i < len(tr.Samples) {
			return int64(tr.Samples[i].DTS), true
		}
	}
	return 0, false
}

// brokenPres fails one track sample, emulating a clock rejection.
type brokenPres struct {
	movie  *mp4.Movie
	track  uint32
	failAt int
}

func (b brokenPres) At(track uint32, i int) (int64, bool) {
	if track == b.track && i == b.failAt {
		return 0, false
	}
	return linearPres{b.movie}.At(track, i)
}

func videoMovie(samples int, syncEvery int, syncTable bool) *mp4.Movie {
	tr := &mp4.Track{ID: 1, Handler: "vide", Entry: "avc1", Timescale: 1000, IndexComplete: true}
	for i := 0; i < samples; i++ {
		tr.Samples = append(tr.Samples, mp4.Sample{
			Index: i, DTS: uint64(i * 100), Duration: 100, Offset: int64(i * 10), Size: 10,
			Sync: syncEvery > 0 && i%syncEvery == 0, InMdat: true,
		})
	}
	tr.SyncTablePresent = syncTable
	return &mp4.Movie{Path: "x.mp4", Tracks: []*mp4.Track{tr}}
}

func TestBuildIndexesOnlySyncSamples(t *testing.T) {
	movie := videoMovie(10, 3, true)
	ix, err := Build(movie, linearPres{movie})
	if err != nil {
		t.Fatal(err)
	}
	if len(ix.Tracks) != 1 {
		t.Fatalf("tracks=%d", len(ix.Tracks))
	}
	ti := ix.Tracks[0]
	if len(ti.Entries) != 4 {
		t.Fatalf("entries=%d want 4", len(ti.Entries))
	}
	if ti.ExactRate() != 1 {
		t.Fatalf("exact rate=%v", ti.ExactRate())
	}
	e, gap, err := ti.Align(550)
	if err != nil {
		t.Fatal(err)
	}
	if e.Sample != 3 || gap != 250 {
		t.Fatalf("align sample=%d gap=%d want 3/250", e.Sample, gap)
	}
	if _, _, err := ti.Align(50); err != nil {
		t.Fatalf("exact hit should align: %v", err)
	}
	if _, _, err := ti.Align(0); err != nil {
		t.Fatalf("first frame should align: %v", err)
	}
}

func TestAlignBeforeFirstKeyframeFails(t *testing.T) {
	movie := videoMovie(5, 0, true) // no sync samples
	ix, err := Build(movie, linearPres{movie})
	if err != nil {
		if !isNoKeyframe(err) {
			t.Fatalf("unexpected error %v", err)
		}
	}
	var ti *TrackIndex
	if len(ix.Tracks) > 0 {
		ti = ix.Tracks[0]
	}
	if ti == nil {
		return
	}
	if _, _, err := ti.Align(10); err == nil {
		t.Fatal("expected no-keyframe error")
	}
}

func isNoKeyframe(err error) bool {
	e, ok := fault.As(err)
	if !ok {
		return false
	}
	return e.Kind == fault.KindIndex && e.Recoverable
}

func TestMissingSyncTableMarksInferred(t *testing.T) {
	movie := videoMovie(5, 0, false) // no stss
	ix, err := Build(movie, linearPres{movie})
	if err != nil {
		t.Fatal(err)
	}
	ti := ix.Tracks[0]
	if len(ti.Entries) != 5 || ti.Inferred != 5 || ti.ExactCount != 0 {
		t.Fatalf("entries=%d inferred=%d exact=%d", len(ti.Entries), ti.Inferred, ti.ExactCount)
	}
	for _, e := range ti.Entries {
		if e.Exact {
			t.Fatalf("entry wrongly exact: %+v", e)
		}
	}
}

func TestClockErrorsBecomeLocatedIndexWarnings(t *testing.T) {
	movie := videoMovie(6, 2, true)
	ix, err := Build(movie, brokenPres{movie: movie, track: 1, failAt: 2})
	if err != nil {
		t.Fatal(err)
	}
	ti := ix.Tracks[0]
	var found bool
	for _, w := range ti.Warnings {
		if w.Sample == 2 && w.TrackID == 1 && w.Kind == fault.KindIndex {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing located warning: %+v", ti.Warnings)
	}
	for _, e := range ti.Entries {
		if e.Sample == 2 {
			t.Fatal("failed sample leaked into index")
		}
	}
}

func TestRebuildByScanUsesChunkStartsAndMarksInferred(t *testing.T) {
	tr := &mp4.Track{ID: 2, Handler: "vide", IndexComplete: false}
	for i := 0; i < 6; i++ {
		tr.Samples = append(tr.Samples, mp4.Sample{Index: i, DTS: uint64(i * 50), Offset: int64(i * 7), Size: 7})
	}
	tr.ChunkOffsets = []int64{0, 14} // chunk starts at samples 0 and 2
	for i := range tr.Samples {
		tr.Samples[i].DTS = uint64(i)
	}
	movie := &mp4.Movie{Path: "y.mp4", Tracks: []*mp4.Track{tr}}
	ti := RebuildByScan(movie, tr, linearPres{movie})
	if !ti.Rebuilt {
		t.Fatal("rebuild flag not set")
	}
	if len(ti.Entries) != 2 || ti.Inferred != 2 || ti.ExactCount != 0 {
		t.Fatalf("entries=%d inferred=%d", len(ti.Entries), ti.Inferred)
	}
	for _, e := range ti.Entries {
		if e.Exact {
			t.Fatalf("rebuilt entry must be inferred: %+v", e)
		}
	}
}
