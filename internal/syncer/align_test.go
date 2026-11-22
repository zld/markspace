package syncer

import (
	"testing"

	"example.com/mediaforge/internal/merrors"
)

func setup(t *testing.T, maxCorr uint64) *Aligner {
	t.Helper()
	a, err := New(Config{MaxCorrectionTicks: maxCorr, ReferenceRate: 90000})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.AddTrack(Track{Kind: KindVideo, SSRC: 1, ClockRate: 90000, OffsetTicks: 0}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddTrack(Track{Kind: KindAudio, SSRC: 2, ClockRate: 48000, OffsetTicks: 0}); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAlignAppliesBaseOffset(t *testing.T) {
	a, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.AddTrack(Track{Kind: KindVideo, SSRC: 1, ClockRate: 90000, OffsetTicks: 4500}); err != nil {
		t.Fatal(err)
	}
	al, err := a.Align(Sample{Track: KindVideo, SSRC: 1, Extended: 10000})
	if err != nil {
		t.Fatal(err)
	}
	if al.CommonTick != 14500 {
		t.Fatalf("common = %d, want 14500", al.CommonTick)
	}
	if !al.InSync {
		t.Fatal("sample flagged out of sync")
	}
	if al.Correction != 4500 {
		t.Fatalf("correction = %d", al.Correction)
	}
}

func TestAlignAudioVideoSkew(t *testing.T) {
	a := setup(t, 9000)
	// Video at 90000 ticks/s, audio at 48000; express both as common ticks.
	v, err := a.Align(Sample{Track: KindVideo, SSRC: 1, Extended: 90000})
	if err != nil {
		t.Fatal(err)
	}
	u, err := a.Align(Sample{Track: KindAudio, SSRC: 2, Extended: 48000})
	if err != nil {
		t.Fatal(err)
	}
	skew, ok := a.Skew(u, v)
	if !ok {
		t.Fatalf("skew %d outside budget", skew)
	}
	// Both samples sit at one second; after conversion to the 90 kHz
	// reference the skew is zero.
	if skew != 0 {
		t.Fatalf("skew = %d, want 0", skew)
	}
}

func TestAlignCorrectionWithinBudget(t *testing.T) {
	a := setup(t, 9000)
	if err := a.AdjustOffset(KindVideo, 3000); err != nil {
		t.Fatal(err)
	}
	al, err := a.Align(Sample{Track: KindVideo, SSRC: 1, Extended: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if al.CommonTick != 4000 {
		t.Fatalf("common = %d, want 4000", al.CommonTick)
	}
	tr, _ := a.Track(KindVideo)
	if tr.OffsetTicks != 3000 {
		t.Fatalf("track offset = %d", tr.OffsetTicks)
	}
	if !a.InSync(KindVideo) {
		t.Fatal("track not in sync")
	}
}

func TestAlignCorrectionExceedsBudget(t *testing.T) {
	a := setup(t, 9000)
	err := a.AdjustOffset(KindVideo, 20000)
	if err == nil {
		t.Fatal("expected refusal")
	}
	if !merrors.IsCategory(err, merrors.OutOfSync) {
		t.Fatalf("category = %v (%v)", err, err)
	}
	if a.InSync(KindVideo) {
		t.Fatal("track still in sync after refusal")
	}
	if a.Stats().Refused != 1 {
		t.Fatalf("stats = %+v", a.Stats())
	}
	// Alignment must be unchanged and the sample keeps its original time.
	al, err := a.Align(Sample{Track: KindVideo, SSRC: 1, Extended: 7000})
	if err == nil {
		t.Fatal("expected out-of-sync error")
	}
	if !merrors.IsCategory(err, merrors.OutOfSync) {
		t.Fatalf("category = %v (%v)", err, err)
	}
	if al.InSync {
		t.Fatal("sample marked in sync")
	}
	if al.CommonTick != 7000 {
		t.Fatalf("original timestamp not preserved: %d", al.CommonTick)
	}
	if al.Sample.Extended != 7000 {
		t.Fatal("sample data altered")
	}
	tr, _ := a.Track(KindVideo)
	if tr.OffsetTicks != 0 {
		t.Fatalf("refused correction applied: %d", tr.OffsetTicks)
	}
}

func TestAlignRecoversAfterRebaseline(t *testing.T) {
	a := setup(t, 9000)
	if err := a.AdjustOffset(KindVideo, 50000); err == nil {
		t.Fatal("expected refusal")
	}
	// An intentional re-baseline goes through AddTrack and clears the flag.
	if err := a.AddTrack(Track{Kind: KindVideo, SSRC: 1, ClockRate: 90000, OffsetTicks: 50000}); err != nil {
		t.Fatal(err)
	}
	al, err := a.Align(Sample{Track: KindVideo, SSRC: 1, Extended: 100})
	if err != nil {
		t.Fatalf("still failing after rebaseline: %v", err)
	}
	if al.CommonTick != 50100 {
		t.Fatalf("common = %d", al.CommonTick)
	}
}

func TestAlignRefusedCorrectionDoesNotAccumulate(t *testing.T) {
	a := setup(t, 1000)
	if err := a.AdjustOffset(KindVideo, 800); err != nil {
		t.Fatal(err)
	}
	// Second shift would exceed the budget in total but each individual delta
	// is compared, so a further 800 is refused.
	if err := a.AdjustOffset(KindVideo, 800); err != nil {
		t.Fatal(err)
	}
	if err := a.AdjustOffset(KindVideo, 5000); err == nil {
		t.Fatal("expected refusal")
	}
	tr, _ := a.Track(KindVideo)
	if tr.OffsetTicks != 1600 {
		t.Fatalf("offset = %d, want 1600", tr.OffsetTicks)
	}
}

func TestAlignTimelineRegression(t *testing.T) {
	a := setup(t, 100)
	if _, err := a.Align(Sample{Track: KindVideo, SSRC: 1, Extended: 100000}); err != nil {
		t.Fatal(err)
	}
	// A small regression is tolerated (reordering within budget).
	al, err := a.Align(Sample{Track: KindVideo, SSRC: 1, Extended: 99950})
	if err != nil {
		t.Fatal(err)
	}
	if !al.InSync || al.CommonTick != 99950 {
		t.Fatalf("aligned = %+v", al)
	}
	// A large regression marks the track out of sync.
	if _, err := a.Align(Sample{Track: KindVideo, SSRC: 1, Extended: 90000}); err == nil {
		t.Fatal("expected regression error")
	}
	if a.InSync(KindVideo) {
		t.Fatal("track should be out of sync")
	}
}

func TestAlignClockRateConversion(t *testing.T) {
	a := setup(t, 90000)
	// 1 second of audio at 48 kHz aligns with 1 second of video at 90 kHz.
	u, err := a.Align(Sample{Track: KindAudio, SSRC: 2, Extended: 48000})
	if err != nil {
		t.Fatal(err)
	}
	v, err := a.Align(Sample{Track: KindVideo, SSRC: 1, Extended: 90000})
	if err != nil {
		t.Fatal(err)
	}
	if u.CommonTick != v.CommonTick {
		t.Fatalf("1s audio=%d, 1s video=%d", u.CommonTick, v.CommonTick)
	}
	// 0.5 s of audio is exactly 45000 reference ticks.
	u, err = a.Align(Sample{Track: KindAudio, SSRC: 2, Extended: 24000})
	if err != nil {
		t.Fatal(err)
	}
	if u.CommonTick != 45000 {
		t.Fatalf("0.5s audio = %d, want 45000", u.CommonTick)
	}
}

func TestAlignUnknownTrackAndSSRC(t *testing.T) {
	a := setup(t, 9000)
	if _, err := a.Align(Sample{Track: "data", Extended: 1}); !merrors.IsCategory(err, merrors.Unsupported) {
		t.Fatalf("unknown track err = %v", err)
	}
	if _, err := a.Align(Sample{Track: KindVideo, SSRC: 99, Extended: 1}); !merrors.IsCategory(err, merrors.Invalid) {
		t.Fatalf("ssrc err = %v", err)
	}
}

func TestAlignOffsetUnderflow(t *testing.T) {
	a, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.AddTrack(Track{Kind: KindAudio, ClockRate: 48000, OffsetTicks: -1000}); err != nil {
		t.Fatal(err)
	}
	al, err := a.Align(Sample{Track: KindAudio, Extended: 10})
	if err == nil {
		t.Fatal("expected underflow error")
	}
	if !merrors.IsCategory(err, merrors.OutOfSync) {
		t.Fatalf("category = %v", err)
	}
	if al.CommonTick != 10 {
		t.Fatalf("common = %d", al.CommonTick)
	}
}

func TestConfigAndTrackValidation(t *testing.T) {
	if _, err := New(Config{MaxCorrectionTicks: 0, ReferenceRate: 90000}); err == nil {
		t.Fatal("expected correction error")
	}
	if _, err := New(Config{MaxCorrectionTicks: 1, ReferenceRate: 0}); err == nil {
		t.Fatal("expected rate error")
	}
	a := setup(t, 100)
	if err := a.AddTrack(Track{Kind: "data", ClockRate: 1}); err == nil {
		t.Fatal("expected kind error")
	}
	if err := a.AddTrack(Track{Kind: KindAudio, ClockRate: 0}); err == nil {
		t.Fatal("expected clock rate error")
	}
	if err := a.AdjustOffset("data", 1); !merrors.IsCategory(err, merrors.Unsupported) {
		t.Fatalf("err = %v", err)
	}
	if a.String() == "" {
		t.Fatal("empty String")
	}
}

func TestAlignDeterministic(t *testing.T) {
	run := func() []uint64 {
		a, _ := New(Config{MaxCorrectionTicks: 5000, ReferenceRate: 90000})
		_ = a.AddTrack(Track{Kind: KindVideo, ClockRate: 90000, OffsetTicks: 1234})
		var out []uint64
		for i := uint64(0); i < 100; i++ {
			al, err := a.Align(Sample{Track: KindVideo, Extended: i * 3000})
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, al.CommonTick)
		}
		return out
	}
	x, y := run(), run()
	for i := range x {
		if x[i] != y[i] {
			t.Fatalf("nondeterministic at %d", i)
		}
	}
}

func TestAlignReset(t *testing.T) {
	a := setup(t, 9000)
	if err := a.AdjustOffset(KindVideo, 100); err != nil {
		t.Fatal(err)
	}
	a.Reset(KindVideo)
	tr, _ := a.Track(KindVideo)
	if tr.OffsetTicks != 0 {
		t.Fatalf("offset after reset = %d", tr.OffsetTicks)
	}
}
