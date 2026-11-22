package syncer

import (
	"testing"

	"mediacore"
)

func TestDriftEstimationClampAndDeterminism(t *testing.T) {
	// Synthetic 180 ppm capture clock: device advances 1.000180 per master ns.
	const targetPPM = 180
	est := NewDriftEstimator(mediacore.TrackAudio1, 300)
	for i := int64(1); i <= 24; i++ {
		master := i * 100 * mediacore.Millisecond
		device := int64(float64(master) * (1.0 + targetPPM/1_000_000.0))
		est.Observe(Anchor{Device: device, Master: master})
	}
	applied, raw, alignable := est.PPM()
	if !alignable || abs64(raw-targetPPM) > 5 {
		t.Fatalf("ppm estimate outside tolerance: raw=%d applied=%d alignable=%v", raw, applied, alignable)
	}
	// Exact rational mapping must be integer-deterministic across repeats.
	m1, ok1 := est.Map(5_000_000_000)
	m2, ok2 := est.Map(5_000_000_000)
	if m1 != m2 || ok1 != ok2 {
		t.Fatal("mapping not deterministic")
	}
	// mulDiv sanity: 1.00018 ratio applied back nearly inverts.
	if got := mulDiv(1_000_180, 1_000_000, 1_000_180); got != 1_000_000 {
		t.Fatalf("mulDiv identity failed: %d", got)
	}
}

func TestDriftOverBoundNotAutoAlignable(t *testing.T) {
	est := NewDriftEstimator(mediacore.TrackAudio1, 100)
	for i := int64(1); i <= 20; i++ {
		master := i * 100 * mediacore.Millisecond
		device := int64(float64(master) * 1.0005) // 500 ppm > 100 bound
		est.Observe(Anchor{Device: device, Master: master})
	}
	applied, _, alignable := est.PPM()
	if alignable {
		t.Fatal("drift over bound marked alignable")
	}
	if applied != 100 {
		t.Fatalf("clamped ppm = %d, want 100", applied)
	}
	err := est.StateError()
	if me, ok := mediacore.AsError(err); !ok || me.Cat != mediacore.CatDrift {
		t.Fatalf("missing classified drift error: %v", err)
	}
}

func TestVFRDuplicatesEmptyAndSorting(t *testing.T) {
	m := NewVFRMapper(mediacore.TrackVideo1, 50*mediacore.Millisecond)
	if err := m.Load(nil); err == nil {
		t.Fatal("empty track accepted")
	} else if me, ok := mediacore.AsError(err); !ok || me.Cat != mediacore.CatEmpty {
		t.Fatalf("empty not classified: %v", err)
	}
	samples := []FrameSample{
		{Index: 0, Time: 0, Keyframe: true},
		{Index: 2, Time: 300_000_000, Keyframe: false}, // VFR: 300ms gap
		{Index: 1, Time: 100_000_000, Keyframe: false}, // out of order
		{Index: 3, Time: 300_000_000, Keyframe: false}, // duplicate timestamp
	}
	if err := m.Load(samples); err != nil {
		t.Fatal(err)
	}
	if m.Duplicates != 1 || len(m.frames) != 4 {
		t.Fatalf("duplicate accounting: dup=%d n=%d", m.Duplicates, len(m.frames))
	}
	if m.frames[0].Index != 0 || m.frames[1].Index != 1 {
		t.Fatal("sample table not deterministically ordered")
	}
	if m.frames[0].Time != 50_000_000 {
		t.Fatal("track start offset not applied")
	}
	// Nearest-frame lookup across a VFR interval.
	f, ok := m.FrameAt(250_000_000 + 50_000_000)
	if !ok || f.Index != 1 {
		t.Fatalf("FrameAt picked %+v ok=%v", f, ok)
	}
	kf := m.Keyframes()
	if len(kf) != 1 || kf[0] != 0 {
		t.Fatalf("keyframe index wrong: %v", kf)
	}
	if len(m.Events()) != 4 {
		t.Fatal("event conversion lost samples")
	}
}

func TestAlignerToleranceAndBoundaries(t *testing.T) {
	m := NewVFRMapper(mediacore.TrackVideo1, 0)
	samples := []FrameSample{
		{Index: 0, Time: 0, Keyframe: true},
		{Index: 1, Time: 100_000_000},
		{Index: 2, Time: 300_000_000}, // VFR gap
	}
	if err := m.Load(samples); err != nil {
		t.Fatal(err)
	}
	al := NewAligner(m, mediacore.TrackAudio1, mediacore.TrackVideo1, 10*mediacore.Millisecond)
	blocks := []mediacore.Event{
		{Track: mediacore.TrackAudio1, Kind: mediacore.KindPCM, Time: -5_000_000},
		{Track: mediacore.TrackAudio1, Kind: mediacore.KindPCM, Time: 95_000_000},
		{Track: mediacore.TrackAudio1, Kind: mediacore.KindPCM, Time: 250_000_000},
		{Track: mediacore.TrackAudio1, Kind: mediacore.KindPCM, Time: 900_000_000},
	}
	rs, err := al.Align(blocks)
	if err != nil || len(rs) != 4 {
		t.Fatalf("align: n=%d err=%v", len(rs), err)
	}
	if rs[0].Status != AlignBeforeFirst || rs[3].Status != AlignAfterLast {
		t.Fatalf("boundary statuses wrong: %d %d", rs[0].Status, rs[3].Status)
	}
	if rs[1].Frame.Index != 1 || rs[1].Offset != -5_000_000 {
		t.Fatalf("nearest-frame offset wrong: %+v", rs[1])
	}
	if rs[2].Status != AlignSkew || rs[1].Status != AlignOK {
		t.Fatal("tolerance classification wrong")
	}
	maxNS, skewed := MaxSkew(rs)
	if skewed != 1 || maxNS != 600_000_000 {
		t.Fatalf("skew summary wrong: max=%d skewed=%d", maxNS, skewed)
	}
}
