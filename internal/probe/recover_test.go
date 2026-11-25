package probe

import (
	"errors"
	"testing"

	"tracecheck/internal/clock"
	"tracecheck/internal/fixture"
)

func recoverSpec() fixture.MediaSpec {
	return fixture.MediaSpec{
		Brand: "isom",
		Tracks: []fixture.TrackSpec{
			{TrackID: 1, Timescale: 48000, SampleCount: 12, SampleDelta: 480,
				PayloadSize: 8, SyncInterval: 4, HandlerType: "soun"},
			{TrackID: 2, Timescale: 24000, SampleCount: 12, SampleDelta: 240,
				PayloadSize: 8, SyncInterval: 4, HandlerType: "vide"},
		},
	}
}

func runRecover(t *testing.T, data []byte) (*RecoveryResult, []*clock.OffsetError) {
	t.Helper()
	nodes, scanFaults := Scan(data)
	tracks, parseFaults := ParseTracks(data, nodes)
	res, err := Recover(data, tracks, Find(nodes, "mdat"))
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	return res, append(scanFaults, parseFaults...)
}

func TestRecoverCleanTimeline(t *testing.T) {
	f := fixture.BuildFile(recoverSpec())
	res, faults := runRecover(t, f.Data)
	if len(faults) != 0 {
		t.Fatalf("unexpected faults: %v", faults)
	}
	if len(res.Tracks) != 2 {
		t.Fatalf("tracks = %d", len(res.Tracks))
	}
	for id, samples := range res.Tracks {
		if len(samples) != 12 {
			t.Fatalf("track %d samples = %d, want 12", id, len(samples))
		}
		for i, s := range samples {
			loc := f.TrackMdat[id][i]
			if int64(s.Offset) != int64(loc.Offset) {
				t.Fatalf("offset rebased: got %d want %d", s.Offset, loc.Offset)
			}
			if s.Start < 0 || (i > 0 && s.Start <= samples[i-1].Start) {
				t.Fatalf("non-monotonic start at %d: %d", i, s.Start)
			}
		}
	}
	// 480/48000 s = 900 ticks; both representations land on the same grid.
	if res.Tracks[1][4].Start != res.Tracks[2][4].Start {
		t.Fatalf("representations diverge: %d vs %d",
			res.Tracks[1][4].Start, res.Tracks[2][4].Start)
	}
}

func TestRecoverCorruptSampleResumesAtSync(t *testing.T) {
	f := fixture.BuildFile(recoverSpec())
	// Sample 5 is non-sync; next sync is sample 8.
	bad := f.CorruptSample(1, 5)
	res, _ := runRecover(t, bad)
	var corrupt, resume bool
	for _, ev := range res.Events {
		if ev.Category == clock.CategoryCorruptSample &&
			int64(ev.Offset) == int64(f.TrackMdat[1][5].Offset) {
			corrupt = true
		}
		if ev.Category == clock.CategoryNoSync &&
			int64(ev.Offset) == int64(f.TrackMdat[1][8].Offset) {
			resume = true
		}
	}
	if !corrupt || !resume {
		t.Fatalf("corrupt=%v resume=%v events=%+v", corrupt, resume, res.Events)
	}
	// Samples 5,6,7 dropped; recovery continues from 8.
	if len(res.Tracks[1]) != 9 {
		t.Fatalf("recovered count = %d, want 9", len(res.Tracks[1]))
	}
	if res.Tracks[1][len(res.Tracks[1])-1].Index != 11 {
		t.Fatal("tail samples lost after resume")
	}
}

func TestRecoverNoSyncPointIsUnrecoverable(t *testing.T) {
	spec := recoverSpec()
	spec.Tracks[0].SyncInterval = 0 // no stss -> every sample treated sync
	f := fixture.BuildFile(spec)
	// Corrupt the final sample: nothing verifiable follows it.
	bad := f.CorruptSample(1, 11)
	nodes, _ := Scan(bad)
	tracks, _ := ParseTracks(bad, nodes)
	_, err := Recover(bad, tracks, Find(nodes, "mdat"))
	var unrecoverable *clock.ErrUnrecoverable
	if !errors.As(err, &unrecoverable) {
		t.Fatalf("want ErrUnrecoverable, got %v", err)
	}
}

func TestRecoverRebuildsFromMagicWhenIndexMissing(t *testing.T) {
	f := fixture.BuildFile(recoverSpec())
	bad := f.RemoveBox(f.FindBoxHeaderForTrack("stco", 0))
	nodes, _ := Scan(bad)
	tracks, _ := ParseTracks(bad, nodes)
	res, err := Recover(bad, tracks, Find(nodes, "mdat"))
	if err != nil {
		t.Fatalf("magic recovery failed: %v", err)
	}
	var sawMissing bool
	for _, ev := range res.Events {
		if ev.Category == clock.CategoryMissingIndex {
			sawMissing = true
		}
	}
	if !sawMissing {
		t.Fatal("missing-index event not recorded")
	}
	if len(res.Tracks[1]) != 12 {
		t.Fatalf("magic-recovered samples = %d, want 12", len(res.Tracks[1]))
	}
	// Recovered offsets must equal the original layout.
	for i, s := range res.Tracks[1] {
		if int64(s.Offset) != int64(f.TrackMdat[1][i].Offset) {
			t.Fatalf("magic offset mismatch at %d", i)
		}
	}
}

func TestRecoverRejectsOutOfFileRange(t *testing.T) {
	f := fixture.BuildFile(recoverSpec())
	// Truncate the mdat payload but keep the full sample table.
	cut := f.TrackMdat[2][10].Offset
	trunc := f.Truncate(cut)
	nodes, _ := Scan(trunc)
	tracks, _ := ParseTracks(trunc, nodes)
	_, err := Recover(trunc, tracks, Find(nodes, "mdat"))
	var oor *clock.ErrOutOfRange
	if !errors.As(err, &oor) {
		t.Fatalf("want bounds error, got %v", err)
	}
	if off, ok := clock.OffsetOf(err); !ok || int64(off) < int64(cut) {
		t.Fatalf("fault offset %d not at/after cut %d", off, cut)
	}
}
