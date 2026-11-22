package timeline

import (
	"errors"
	"testing"

	"archivist/internal/model"
)

func TestClockRejectsZeroAndOverflow(t *testing.T) {
	if _, err := NewClock(0); !errors.Is(err, model.ErrZeroTimescale) {
		t.Fatalf("zero timescale: %v", err)
	}
	c, _ := NewClock(48000)
	// A value near uint64 max cannot multiply by another scale safely.
	if _, err := c.Convert(^uint64(0)>>1, Clock{timescale: 90000}); !errors.Is(err, model.ErrTimestampOverflow) {
		t.Fatalf("expected overflow, got %v", err)
	}
}

func TestExactRationalConversion(t *testing.T) {
	c24, _ := NewClock(24000)
	c48, _ := NewClock(48000)
	got, err := c24.Convert(24000, c48)
	if err != nil || got != 48000 {
		t.Fatalf("24k->48k = %d, %v", got, err)
	}
	// Non-integer relationship must still map exactly on the LCM clock.
	common, err := CommonTimescale([]uint32{24000, 44100})
	if err != nil {
		t.Fatal(err)
	}
	if common != lcmRef(24000, 44100) {
		t.Fatalf("common timescale = %d", common)
	}
}

func TestMonotonicRejection(t *testing.T) {
	err := VerifyMonotonic(7, 1000, []uint64{0, 100, 100, 300})
	if !errors.Is(err, model.ErrNonMonotonic) {
		t.Fatalf("expected non-monotonic, got %v", err)
	}
	var ae *model.ArchiveError
	if !errors.As(err, &ae) || ae.TrackID != 7 || ae.SampleIdx != 2 {
		t.Fatalf("error must carry track/sample position: %+v", err)
	}
}

func videoTrack(id uint32, ts uint32, keyDTS []uint64) TrackClock {
	tr := &model.Track{ID: id, Kind: model.KindVideo, Timescale: ts}
	c, _ := NewClock(ts)
	return TrackClock{Track: tr, Clock: c, KeyDTS: keyDTS}
}

func TestAlignedBoundariesAcrossBitrates(t *testing.T) {
	// Both representations share keyframes at 0,2,4,... seconds but use
	// different timescales (24000 vs 48000).
	low := videoTrack(1, 24000, []uint64{0, 48000, 96000, 144000, 192000, 240000})
	high := videoTrack(2, 48000, []uint64{0, 96000, 192000, 288000, 384000, 480000})
	res, err := AlignBoundaries([]TrackClock{low, high}, 6)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Aligned || len(res.Boundaries) != 6 || len(res.Drifts) != 0 {
		t.Fatalf("alignment result wrong: %+v", res)
	}
	// Per-track ticks must map back to the native keyframe DTS.
	if res.Boundaries[1].PerTrackTicks[1] != 48000 ||
		res.Boundaries[1].PerTrackTicks[2] != 96000 {
		t.Fatalf("native tick mapping wrong: %+v", res.Boundaries[1].PerTrackTicks)
	}
}

func TestKeyframeDriftBlocksAndReports(t *testing.T) {
	low := videoTrack(1, 24000, []uint64{0, 48000, 96000, 144000})
	// High GOP is shifted: no keyframe at the low track's grid points.
	high := videoTrack(2, 48000, []uint64{24000, 120000, 216000, 312000})
	res, err := AlignBoundaries([]TrackClock{low, high}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if res.Aligned {
		t.Fatal("drifted keyframes must not be reported as aligned")
	}
	if len(res.Boundaries) != 0 || len(res.Drifts) == 0 {
		t.Fatalf("expected zero clean boundaries and drift evidence: %+v", res)
	}
	if res.Drifts[0].TrackID != 2 || res.Drifts[0].DeviationTicks == 0 {
		t.Fatalf("drift must name the offending track and quantify deviation: %+v", res.Drifts[0])
	}
}

func TestAudioLayoutAndRateDecisions(t *testing.T) {
	layout, dec := NormalizeLayout(3, model.ChannelLayout{Count: 6, SampleRate: 48000})
	if !layout.Valid || len(layout.Labels) != 6 || dec.Action != "passthrough" {
		t.Fatalf("5.1 layout normalization wrong: %+v %+v", layout, dec)
	}
	ratio, rateDec, warn := NormalizeSampleRate(3, 44100)
	// 48000/44100 reduces canonically to 160/147; compare exact value.
	if ratio.Cmp(mustRat(t, 48000, 44100)) != 0 || ratio.Num != 160 || ratio.Den != 147 {
		t.Fatalf("sample rate ratio = %s", ratio)
	}
	if rateDec.Action != "map" || warn.Code != "sample-rate-mismatch" {
		t.Fatalf("rate decision and warning wrong: %+v %+v", rateDec, warn)
	}
}

func TestColorConflictByPriority(t *testing.T) {
	ref := model.ColorMeta{Present: true, ColorType: "nclc", Primaries: 1, Transfer: 1, Matrix: 1}
	conflict := model.ColorMeta{Present: true, ColorType: "nclc", Primaries: 9, Transfer: 16, Matrix: 9}
	dec, warns := ColorDecision(2, 2, conflict, ref)
	if dec.Action != "conflict-resolved" || len(warns) != 1 || warns[0].Code != "color-conflict" {
		t.Fatalf("color conflict handling wrong: %+v %+v", dec, warns)
	}
	missing, mwarns := ColorDecision(3, 3, model.ColorMeta{Present: false}, ref)
	if missing.Action != "warning" || len(mwarns) != 1 || mwarns[0].Code != "color-missing" {
		t.Fatalf("missing color handling wrong: %+v %+v", missing, mwarns)
	}
}

func lcmRef(a, b uint64) uint64 {
	return a / gcdRef(a, b) * b
}

func mustRat(t *testing.T, num int64, den uint64) model.Rat {
	t.Helper()
	r, err := model.NewRat(num, den)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func gcdRef(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
