package timeline_test

import (
	"testing"

	"mediaroute/internal/fixture"
	"mediaroute/internal/ingest"
	"mediaroute/internal/model"
	"mediaroute/internal/timeline"
)

func load(t *testing.T, d fixture.Damage) *model.SourceSet {
	t.Helper()
	g, err := fixture.Generate(fixture.DefaultParams(17), d)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	set, err := ingest.Read(g.Bytes, ingest.DefaultConfig())
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	return set
}

func TestRepairCleanIsMonotonicAndUnchanged(t *testing.T) {
	set := load(t, fixture.DamageNone)
	res, err := timeline.Repair(set)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if res.RepairedTfdt != 0 || res.MFHDGaps != 0 {
		t.Fatalf("clean stream should need no repairs, got tfdt=%d gaps=%d",
			res.RepairedTfdt, res.MFHDGaps)
	}
	assertMonotonic(t, set)
}

func TestRepairSequenceGapCountedWithoutFabrication(t *testing.T) {
	set := load(t, fixture.DamageSequenceGap)
	before := totalSamples(set)
	res, err := timeline.Repair(set)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if res.MFHDGaps != 1 {
		t.Fatalf("want 1 mfhd gap, got %d", res.MFHDGaps)
	}
	if after := totalSamples(set); after != before {
		t.Fatalf("gap repair must not fabricate samples: %d -> %d", before, after)
	}
	assertMonotonic(t, set)
}

func TestRepairTfdtRegression(t *testing.T) {
	set := load(t, fixture.DamageTfdtRegression)
	res, err := timeline.Repair(set)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if res.RepairedTfdt < 2 {
		t.Fatalf("want at least one repair per track (2 tracks), got %d", res.RepairedTfdt)
	}
	if len(res.Repairs) != res.RepairedTfdt {
		t.Fatalf("repair records (%d) disagree with count (%d)", len(res.Repairs), res.RepairedTfdt)
	}
	for _, rec := range res.Repairs {
		if rec.Kind != "tfdt_regression" {
			t.Fatalf("unexpected repair kind %q", rec.Kind)
		}
		if rec.RepairedValue <= rec.OriginalValue {
			t.Fatalf("repair did not move the tfdt forward: %+v", rec)
		}
		if rec.SourceRange.Empty() {
			t.Fatalf("repair record lacks a source range: %+v", rec)
		}
	}
	assertMonotonic(t, set)
	// Exact alignment: the repaired fragment must start at the previous end.
	for i := 1; i < len(set.Fragments); i++ {
		prev := &set.Fragments[i-1]
		cur := &set.Fragments[i]
		if prev.Discarded || cur.Discarded {
			continue
		}
		for ti := range cur.Tracks {
			ft := &cur.Tracks[ti]
			var prevEnd model.Micros
			found := false
			for _, pt := range prev.Tracks {
				if pt.TrackID == ft.TrackID {
					prevEnd = pt.EndMicros
					found = true
				}
			}
			if !found {
				continue
			}
			if ft.StartMicros < prevEnd {
				t.Fatalf("fragment %d track %d overlaps previous end", i, ft.TrackID)
			}
			if model.AbsMicros(ft.StartMicros, prevEnd) > 1 {
				t.Fatalf("fragment %d track %d misaligned by %d us",
					i, ft.TrackID, model.AbsMicros(ft.StartMicros, prevEnd))
			}
		}
	}
}

func TestRepairRejectsMissingInit(t *testing.T) {
	set := load(t, fixture.DamageMissingInit)
	if _, err := timeline.Repair(set); err == nil {
		t.Fatal("expected repair to reject a stream with no init segment")
	}
}

func TestRepairRejectsZeroTimescale(t *testing.T) {
	set := load(t, fixture.DamageZeroTimescale)
	_, err := timeline.Repair(set)
	te, ok := model.AsTaskError(err)
	if !ok || te.Category != model.CategoryZeroTimescale {
		t.Fatalf("want zero_timescale, got %v", err)
	}
}

func TestRepairSkipsDiscardedFragmentsWithoutAdvancingClock(t *testing.T) {
	set := load(t, fixture.DamageTruncatedBox)
	res, err := timeline.Repair(set)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if res.MFHDGaps != 1 {
		// The truncated fragment carried sequence 4; dropping it leaves a gap.
		t.Fatalf("want 1 gap after dropping the damaged fragment, got %d", res.MFHDGaps)
	}
	assertMonotonic(t, set)
	// The fragment immediately after the dropped one must start exactly at the
	// previous fragment's end, proving the clock skipped the discarded range.
	var prevEnd model.Micros
	sawDropped := false
	checked := false
	for i := range set.Fragments {
		f := &set.Fragments[i]
		if f.Discarded {
			sawDropped = true
			continue
		}
		if sawDropped && !checked {
			for _, ft := range f.Tracks {
				if ft.StartMicros < prevEnd {
					t.Fatalf("first recovered fragment starts inside dropped range")
				}
			}
			checked = true
		}
		for _, ft := range f.Tracks {
			if ft.EndMicros > prevEnd {
				prevEnd = ft.EndMicros
			}
		}
	}
	if !checked {
		t.Fatal("no fragment followed the discarded one")
	}
}

func assertMonotonic(t *testing.T, set *model.SourceSet) {
	t.Helper()
	last := make(map[uint32]model.Micros, 4)
	for _, f := range set.Fragments {
		if f.Discarded || f.Truncated {
			continue
		}
		for _, ft := range f.Tracks {
			if prev, ok := last[ft.TrackID]; ok && ft.StartMicros < prev {
				t.Fatalf("track %d fragment %d starts %d us before previous end %d us",
					ft.TrackID, f.Index, ft.StartMicros, prev)
			}
			if ft.EndMicros <= ft.StartMicros {
				t.Fatalf("track %d fragment %d has non-positive duration", ft.TrackID, f.Index)
			}
			last[ft.TrackID] = ft.EndMicros
		}
	}
}

func totalSamples(set *model.SourceSet) int {
	n := 0
	for _, f := range set.Fragments {
		n += f.SampleCount()
	}
	return n
}

// TestForwardMediaGapPreserved proves that a tfdt that legitimately jumps
// forward (a real gap in the media, not a regression) is left untouched.
func TestForwardMediaGapPreserved(t *testing.T) {
	set := &model.SourceSet{
		Init:   &model.InitSegment{Bytes: []byte{1, 2, 3, 4}, Tracks: []model.Track{{TrackID: 1, Timescale: 90000}}},
		Tracks: []model.Track{{TrackID: 1, Timescale: 90000}},
		Fragments: []model.Fragment{
			{Index: 0, Sequence: 1, Tracks: []model.FragmentTrack{{
				TrackID: 1, TfdtPresent: true, Tfdt: 0, SequenceKnown: true,
				Samples: []model.Sample{{Duration: 1800, Size: 10}},
			}}},
			{Index: 1, Sequence: 2, Tracks: []model.FragmentTrack{{
				TrackID: 1, TfdtPresent: true, Tfdt: 90000, SequenceKnown: true,
				Samples: []model.Sample{{Duration: 1800, Size: 10}},
			}}},
		},
	}
	res, err := timeline.Repair(set)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if res.RepairedTfdt != 0 {
		t.Fatalf("forward gap must not be repaired, got %d repairs", res.RepairedTfdt)
	}
	if got := set.Fragments[1].Tracks[0].StartMicros; got != 1_000_000 {
		t.Fatalf("second fragment start = %d us, want 1000000 (gap preserved)", got)
	}
}

// TestEmptyTrafIsSkipped proves that a traf carrying no samples does not
// panic and does not advance the track clock (it is legal but contentless).
func TestEmptyTrafIsSkipped(t *testing.T) {
	set := &model.SourceSet{
		Init:   &model.InitSegment{Bytes: []byte{1, 2, 3, 4}, Tracks: []model.Track{{TrackID: 1, Timescale: 90000}}},
		Tracks: []model.Track{{TrackID: 1, Timescale: 90000}},
		Fragments: []model.Fragment{
			{Index: 0, Sequence: 1, Tracks: []model.FragmentTrack{{TrackID: 1, TfdtPresent: true, Tfdt: 0, SequenceKnown: true}}},
			{Index: 1, Sequence: 2, Tracks: []model.FragmentTrack{{
				TrackID: 1, TfdtPresent: true, Tfdt: 0, SequenceKnown: true,
				Samples: []model.Sample{{Duration: 1800, Size: 10}},
			}}},
		},
	}
	res, err := timeline.Repair(set)
	if err != nil {
		t.Fatalf("repair with empty traf: %v", err)
	}
	if res.RepairedTfdt != 0 {
		t.Fatalf("empty traf must not trigger repair, got %d", res.RepairedTfdt)
	}
	// The second fragment starts at 0 because the empty one advanced nothing.
	if got := set.Fragments[1].Tracks[0].StartMicros; got != 0 {
		t.Fatalf("second fragment start = %d, want 0", got)
	}
}
