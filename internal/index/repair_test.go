package index

import (
	"testing"

	"github.com/pairwise/mediad/internal/mp4"
)

// linearPres is defined in keyframes_test.go and reused here.

func repairMovie(samples, keyEvery int, chunkEvery int) *mp4.Movie {
	tr := &mp4.Track{ID: 1, Handler: "vide", IndexComplete: true, SyncTablePresent: true}
	for i := 0; i < samples; i++ {
		tr.Samples = append(tr.Samples, mp4.Sample{Index: i, DTS: uint64(i * 100), Offset: int64(i * 8), Size: 8})
		if chunkEvery > 0 && i%chunkEvery == 0 {
			tr.ChunkOffsets = append(tr.ChunkOffsets, int64(i*8))
		}
	}
	return &mp4.Movie{Path: "r.mp4", Tracks: []*mp4.Track{tr}}
}

func TestRepairExtendsTruncatedSyncTableWithInferredEntries(t *testing.T) {
	movie := repairMovie(120, 12, 12)
	// Index only the first 5 sync frames, as a damaged stss would.
	ti := &TrackIndex{TrackID: 1}
	for i := 0; i < 60; i += 12 {
		ti.Entries = append(ti.Entries, Entry{TrackID: 1, Sample: i, Presented: int64(i * 100), Exact: true})
		ti.ExactCount++
	}
	ix := &Index{Path: "r.mp4", Tracks: []*TrackIndex{ti}}
	if err := Repair(ix, movie, linearPres{movie}); err != nil {
		t.Fatal(err)
	}
	if !ti.Rebuilt || !ix.Rebuilt {
		t.Fatal("repair flag not set")
	}
	if ti.ExactCount != 5 {
		t.Fatalf("exact count changed to %d", ti.ExactCount)
	}
	if len(ti.Entries) <= 5 || ti.Inferred == 0 {
		t.Fatalf("no inferred entries: entries=%d inferred=%d", len(ti.Entries), ti.Inferred)
	}
	last := ti.Entries[len(ti.Entries)-1]
	if last.Exact {
		t.Fatal("repaired entry marked exact")
	}
	if last.Presented <= int64(59*100) {
		t.Fatalf("repair did not extend past the damage: %+v", last)
	}
	for i := 1; i < len(ti.Entries); i++ {
		if ti.Entries[i].Presented < ti.Entries[i-1].Presented {
			t.Fatalf("entries unordered after repair: %+v", ti.Entries)
		}
	}
}

func TestRepairLeavesCompleteIndexAlone(t *testing.T) {
	movie := repairMovie(60, 12, 12)
	ti := &TrackIndex{TrackID: 1}
	for i := 0; i < 60; i += 12 {
		ti.Entries = append(ti.Entries, Entry{TrackID: 1, Sample: i, Presented: int64(i * 100), Exact: true})
		ti.ExactCount++
	}
	// Final sample at 59 is one frame past the last sync at 48, a normal tail.
	ix := &Index{Path: "r.mp4", Tracks: []*TrackIndex{ti}}
	if err := Repair(ix, movie, linearPres{movie}); err != nil {
		t.Fatal(err)
	}
	if ti.Rebuilt || ti.Inferred != 0 {
		t.Fatalf("complete index was rewritten: %+v", ti)
	}
}

func TestRepairRespectsGOPSpacing(t *testing.T) {
	// One sample per chunk must not become one inferred entry per sample.
	movie := repairMovie(120, 12, 1)
	ti := &TrackIndex{TrackID: 1}
	for i := 0; i < 36; i += 12 {
		ti.Entries = append(ti.Entries, Entry{TrackID: 1, Sample: i, Presented: int64(i * 100), Exact: true})
		ti.ExactCount++
	}
	ix := &Index{Path: "r.mp4", Tracks: []*TrackIndex{ti}}
	if err := Repair(ix, movie, linearPres{movie}); err != nil {
		t.Fatal(err)
	}
	added := ti.Inferred
	if added == 0 {
		t.Fatal("repair added nothing")
	}
	// Spacing is 12, so roughly (120-35)/12 entries, definitely not ~85.
	if added > 12 {
		t.Fatalf("repair ignored GOP spacing: added %d", added)
	}
}

func TestRepairDoesNotInventTimesWithoutPresentation(t *testing.T) {
	movie := repairMovie(24, 12, 12)
	ti := &TrackIndex{TrackID: 1}
	ti.Entries = append(ti.Entries, Entry{TrackID: 1, Sample: 0, Presented: 0, Exact: true})
	ti.ExactCount = 1
	ix := &Index{Path: "r.mp4", Tracks: []*TrackIndex{ti}}
	// A presentation that knows only sample 0 permits no inferred entries.
	if err := Repair(ix, movie, onlyOne{movie}); err != nil {
		t.Fatal(err)
	}
	if ti.Inferred != 0 {
		t.Fatalf("invented entries without timestamps: %d", ti.Inferred)
	}
}

type onlyOne struct{ movie *mp4.Movie }

func (o onlyOne) At(track uint32, i int) (int64, bool) {
	if i == 0 {
		return 0, true
	}
	return 0, false
}
