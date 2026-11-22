package timeline

import (
	"testing"

	"avcheck/internal/media"
)

func mkSlotFrame(stream uint32, seq uint64, ticks int64, state media.FrameState) *media.Frame {
	return &media.Frame{
		StreamID: stream, Seq: seq, Ticks: ticks,
		Meta:  media.Meta{Version: media.MetaVersion},
		State: state,
	}
}

func TestStreamComposerMatchesBatch(t *testing.T) {
	clock, _ := media.NewClock(90000)
	ids := []uint32{2, 1}
	lo, hi := uint64(100), uint64(114)
	sc, err := NewStreamComposer(clock, ids, lo, hi, 64)
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.SetState(2, TrackRecovered); err != nil {
		t.Fatal(err)
	}
	var got []OutputFrame
	emit := func(of OutputFrame) error { got = append(got, of); return nil }
	for i := uint64(0); i < 10; i++ {
		if err := sc.Offer(2, mkSlotFrame(2, 100+i, int64(i)*3000+9000, media.StateClean)); err != nil {
			t.Fatal(err)
		}
		if i < 9 {
			if err := sc.Offer(1, mkSlotFrame(1, 105+i, int64(i)*3000, media.StateClean)); err != nil {
				t.Fatal(err)
			}
		}
		if err := sc.Drain(emit); err != nil {
			t.Fatal(err)
		}
		if sc.PendingPeak > 10 {
			t.Fatalf("pending peak %d", sc.PendingPeak)
		}
	}
	if err := sc.Flush(emit); err != nil {
		t.Fatal(err)
	}
	if len(got) != 15 {
		t.Fatalf("slots=%d want 15", len(got))
	}
	// batch composer over the same data must agree exactly
	a, b := NewTrack(2), NewTrack(1)
	for i := uint64(0); i < 10; i++ {
		if err := a.Add(mkSlotFrame(2, 100+i, int64(i)*3000+9000, media.StateClean)); err != nil {
			t.Fatal(err)
		}
		if i < 9 {
			if err := b.Add(mkSlotFrame(1, 105+i, int64(i)*3000, media.StateClean)); err != nil {
				t.Fatal(err)
			}
		}
	}
	want, err := NewComposer(clock, a, b).Compose()
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		if got[i].Index != want[i].Index || got[i].Time.Cmp(want[i].Time) != 0 {
			t.Fatalf("slot %d: got %v/%v want %v/%v", i, got[i].Index, got[i].Time, want[i].Index, want[i].Time)
		}
		if len(got[i].Sources) != len(want[i].Sources) {
			t.Fatalf("slot %d sources %d != %d", i, len(got[i].Sources), len(want[i].Sources))
		}
		for k := range want[i].Sources {
			if got[i].Sources[k].TrackID != want[i].Sources[k].TrackID ||
				got[i].Sources[k].Frame != want[i].Sources[k].Frame {
				t.Fatalf("slot %d src %d: %+v want %+v", i, k, got[i].Sources[k], want[i].Sources[k])
			}
		}
	}
}

func TestStreamComposerPlaceholderAndCommit(t *testing.T) {
	clock, _ := media.NewClock(90000)
	sc, err := NewStreamComposer(clock, []uint32{1}, 10, 12, 8)
	if err != nil {
		t.Fatal(err)
	}
	// seq 11 arrives before 10: a gap was committed upstream, so slot 10 can
	// be decided as a placeholder immediately
	if err := sc.Offer(1, mkSlotFrame(1, 11, 3000, media.StateRecovered)); err != nil {
		t.Fatal(err)
	}
	var out []OutputFrame
	if err := sc.Drain(func(of OutputFrame) error { out = append(out, of); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Index != 10 || out[0].Sources[0].Frame != media.StatePlaceholder {
		t.Fatalf("gap slot must be a placeholder: %+v", out)
	}
	if err := sc.Offer(1, mkSlotFrame(1, 12, 6000, media.StateClean)); err != nil {
		t.Fatal(err)
	}
	if err := sc.Drain(func(of OutputFrame) error { out = append(out, of); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("drained %d want 2", len(out))
	}
	if out[1].Sources[0].Frame != media.StateRecovered {
		t.Fatalf("11 must stay recovered: %+v", out[1].Sources[0])
	}
	// committed index cannot be rewritten
	if err := sc.Offer(1, mkSlotFrame(1, 10, 0, media.StateClean)); err == nil {
		t.Fatal("late frame for committed slot must be rejected")
	}
	// out-of-order per track is rejected
	if err := sc.Offer(1, mkSlotFrame(1, 12, 9000, media.StateClean)); err == nil {
		t.Fatal("duplicate sequence must be rejected")
	}
	if err := sc.Flush(func(of OutputFrame) error { out = append(out, of); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || out[2].Index != 12 {
		t.Fatalf("flush produced %d slots, last %+v", len(out), out[len(out)-1])
	}
}

func TestStreamComposerLookaheadGuard(t *testing.T) {
	clock, _ := media.NewClock(90000)
	sc, err := NewStreamComposer(clock, []uint32{1, 2}, 0, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	// track 2 never delivers: frames on track 1 accumulate and trip the guard
	if err := sc.Offer(1, mkSlotFrame(1, 0, 0, media.StateClean)); err != nil {
		t.Fatal(err)
	}
	if err := sc.Offer(1, mkSlotFrame(1, 1, 1, media.StateClean)); err != nil {
		t.Fatal(err)
	}
	err = sc.Offer(1, mkSlotFrame(1, 2, 2, media.StateClean))
	me, ok := err.(*media.Error)
	if !ok || me.Kind != media.KindRange || me.Track != 1 {
		t.Fatalf("expected lookahead guard, got %v", err)
	}
	if _, err := NewStreamComposer(clock, []uint32{1, 1}, 0, 1, 4); err == nil {
		t.Fatal("duplicate track ids must be rejected")
	}
	if _, err := NewStreamComposer(clock, nil, 0, 1, 4); err == nil {
		t.Fatal("empty track set must be rejected")
	}
}
