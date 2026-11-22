package timeline

import (
	"testing"

	"avcheck/internal/media"
)

func mkFrame(stream uint32, seq uint64, ticks int64, state media.FrameState) *media.Frame {
	return &media.Frame{
		StreamID: stream, Seq: seq, Ticks: ticks,
		Meta:  media.Meta{Version: media.MetaVersion},
		State: state,
	}
}

func TestComposeMonotonicWithSkewedStarts(t *testing.T) {
	clock, _ := media.NewClock(90000)
	a, b := NewTrack(2), NewTrack(1) // ids out of order on purpose
	// track 1 starts later (seq 105) and at lower ticks
	for i := uint64(0); i < 10; i++ {
		if err := a.Add(mkFrame(2, 100+i, int64(i)*3000+9000, media.StateClean)); err != nil {
			t.Fatal(err)
		}
	}
	for i := uint64(0); i < 10; i++ {
		if err := b.Add(mkFrame(1, 105+i, int64(i)*3000, media.StateClean)); err != nil {
			t.Fatal(err)
		}
	}
	out, err := NewComposer(clock, a, b).Compose()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 15 { // span 100..114
		t.Fatalf("len=%d want 15", len(out))
	}
	for i := 1; i < len(out); i++ {
		if out[i].Time.Cmp(out[i-1].Time) < 0 {
			t.Fatalf("time regression at %d: %v < %v", i, out[i].Time, out[i-1].Time)
		}
	}
	// deterministic source order: track 1 first
	if out[0].Sources[0].TrackID != 1 || out[0].Sources[1].TrackID != 2 {
		t.Fatalf("source order: %+v", out[0].Sources)
	}
	// seq 100: track 1 not started -> placeholder, track 2 present
	if out[0].Sources[0].Frame != media.StatePlaceholder {
		t.Fatalf("expected placeholder for late-starting track: %+v", out[0].Sources[0])
	}
	if out[0].Primary == nil || out[0].Primary.StreamID != 2 {
		t.Fatal("primary must come from the only present track")
	}
}

func TestComposeGapPlaceholderAndNoRewrite(t *testing.T) {
	clock, _ := media.NewClock(90000)
	tr := NewTrack(1)
	for i := uint64(0); i < 6; i++ {
		if i == 3 { // burst-loss hole
			continue
		}
		st := media.StateClean
		if i == 4 {
			st = media.StateRecovered
		}
		if err := tr.Add(mkFrame(1, 200+i, int64(i)*3000, st)); err != nil {
			t.Fatal(err)
		}
	}
	tr.Degrade()
	tr.Recover()
	out, err := NewComposer(clock, tr).Compose()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 6 {
		t.Fatalf("len=%d", len(out))
	}
	if out[3].Sources[0].Frame != media.StatePlaceholder || out[3].Primary != nil {
		t.Fatalf("index 203 must be placeholder: %+v", out[3].Sources[0])
	}
	if out[4].Sources[0].Frame != media.StateRecovered {
		t.Fatalf("index 204 must be recovered: %+v", out[4].Sources[0])
	}
	if out[0].Sources[0].Track != TrackRecovered {
		t.Fatalf("track state must read recovered: %v", out[0].Sources[0].Track)
	}
	// committed timeline cannot be rewritten by a late arrival
	late := mkFrame(1, 203, 9999, media.StateClean)
	if err := tr.Add(late); err == nil || err.(*media.Error).Kind != media.KindState {
		t.Fatalf("rewrite of committed seq must fail: %v", err)
	}
	// monotonic across the hole
	for i := 1; i < len(out); i++ {
		if out[i].Time.Cmp(out[i-1].Time) < 0 {
			t.Fatalf("regression at %d", i)
		}
	}
}

func TestComposeMissingKeyframeStillMonotonic(t *testing.T) {
	clock, _ := media.NewClock(90000)
	tr := NewTrack(1)
	for i := uint64(0); i < 8; i++ {
		f := mkFrame(1, 500+i, int64(i)*3000, media.StateClean)
		f.Key = i%4 == 0
		if i == 4 { // keyframe lost to corruption
			continue
		}
		if err := tr.Add(f); err != nil {
			t.Fatal(err)
		}
	}
	out, err := NewComposer(clock, tr).Compose()
	if err != nil {
		t.Fatal(err)
	}
	if out[4].Sources[0].Frame != media.StatePlaceholder {
		t.Fatal("lost keyframe must be a placeholder")
	}
	for i := 1; i < len(out); i++ {
		if out[i].Time.Cmp(out[i-1].Time) < 0 {
			t.Fatalf("regression at %d", i)
		}
	}
}

func TestComposeEmpty(t *testing.T) {
	clock, _ := media.NewClock(90000)
	out, err := NewComposer(clock, NewTrack(1)).Compose()
	if err != nil || len(out) != 0 {
		t.Fatalf("empty compose: %v %d", err, len(out))
	}
}
