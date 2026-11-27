package sync_test

import (
	"testing"

	"mediasync/internal/fixture"
	"mediasync/internal/index"
	"mediasync/internal/model"
	"mediasync/internal/sync"
)

func scanTracks(t *testing.T) *index.ScanResult {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/m.mp4"
	specs := []fixture.TrackSpec{
		{Handler: "soun", TrackID: 1, Timescale: 48000, Deltas: []uint32{4800, 4800, 4800, 4800, 4800}},
		{Handler: "subt", TrackID: 2, Timescale: 1000, Deltas: []uint32{100, 100, 100, 100, 100}},
	}
	if _, err := fixture.WriteMP4(path, 1000, specs); err != nil {
		t.Fatal(err)
	}
	res, err := index.Scan(path, model.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestAlignPerfectSync(t *testing.T) {
	res := scanTracks(t)
	audio := res.FindTrack("soun")
	sub := res.FindTrack("subt")
	cues := sync.BuildCuesFromTrack(sub)
	rep, err := sync.Align(audio, cues)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Matched != 5 || rep.Missing != 0 {
		t.Fatalf("want 5 matched 0 missing, got %d/%d", rep.Matched, rep.Missing)
	}
	if rep.MaxAbsErrNS != 0 || rep.P95ErrNS != 0 {
		t.Fatalf("synthetic aligned tracks must have zero error, got max=%d p95=%d",
			rep.MaxAbsErrNS, rep.P95ErrNS)
	}
}

func TestAlignBoundedJitter(t *testing.T) {
	res := scanTracks(t)
	audio := res.FindTrack("soun")
	sub := res.FindTrack("subt")
	// Every cue leads by 500 us: within the 1 ms acceptance budget.
	jitter := []model.Ticks{
		500 * model.Milli / 1000,
		500 * model.Milli / 1000,
		500 * model.Milli / 1000,
		500 * model.Milli / 1000,
		500 * model.Milli / 1000,
	}
	cues := sync.BuildCuesWithJitter(sub, jitter)
	rep, err := sync.Align(audio, cues)
	if err != nil {
		t.Fatal(err)
	}
	want := model.Ticks(500_000)
	if rep.MaxAbsErrNS != want {
		t.Fatalf("max error %d, want %d", rep.MaxAbsErrNS, want)
	}
	for _, cr := range rep.Cues {
		if cr.SyncErrNS != want || cr.AbsErrNS != want {
			t.Fatalf("signed/abs error mismatch: %+v", cr)
		}
	}
}

func TestAlignMissingCue(t *testing.T) {
	res := scanTracks(t)
	audio := res.FindTrack("soun")
	cues := []sync.Cue{
		{ID: "in", StartNS: 100 * model.Milli, EndNS: 200 * model.Milli},
		{ID: "after", StartNS: 5 * model.Nano, EndNS: 6 * model.Nano},
		{ID: "before", StartNS: -model.Milli, EndNS: 0},
	}
	rep, err := sync.Align(audio, cues)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Matched != 1 || rep.Missing != 2 {
		t.Fatalf("want 1 matched 2 missing, got %d/%d", rep.Matched, rep.Missing)
	}
	if !rep.Cues[1].Missing || !rep.Cues[2].Missing {
		t.Fatalf("out-of-range cues must be flagged missing: %+v", rep.Cues)
	}
}

func TestAlignNoAudio(t *testing.T) {
	if _, err := sync.Align(nil, nil); model.AsMediaError(err) == nil ||
		model.AsMediaError(err).Class != model.ClassMissingTimestamp {
		t.Fatalf("want missing_timestamp error, got %v", err)
	}
}
