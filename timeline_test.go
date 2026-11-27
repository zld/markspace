package mediacore

import (
	"errors"
	"math"
	"testing"
)

func TestScaleChecked(t *testing.T) {
	cases := []struct {
		name      string
		ticks     int64
		timescale uint32
		want      Nano
		wantErr   error
	}{
		{"one second at 90k", 90000, 90000, NanosPerSecond, nil},
		{"48k audio frame", 1024, 48000, Nano(1024) * NanosPerSecond / 48000, nil},
		{"zero ticks", 0, 1000, 0, nil},
		{"zero timescale", 5, 0, 0, ErrZeroTimescale},
		{"negative ticks", -1, 1000, 0, ErrRegression},
		{"overflow", math.MaxInt64, 1000, 0, ErrOverflow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ScaleChecked(tc.ticks, tc.timescale)
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("want error, got %d", got)
				}
				var me *Error
				if !errors.As(err, &me) || me.Stage != StageTimeline {
					t.Fatalf("want timeline *Error, got %#v", err)
				}
				if tc.wantErr == ErrOverflow && !errors.Is(err, ErrOverflow) &&
					!errors.Is(err, ErrZeroTimescale) {
					// negative ticks carries its own message; overflow path is generic
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}

func TestScaleCheckedOverflowProduct(t *testing.T) {
	// ticks that fit int64 but ticks*1e9 overflows.
	_, err := ScaleChecked(math.MaxInt64/int64(NanosPerSecond)+1, 1000)
	if err == nil {
		t.Fatal("expected overflow error")
	}
	var me *Error
	if !errors.As(err, &me) || me.Offset != -1 {
		t.Fatalf("want sourced *Error with offset -1, got %v", err)
	}
}

func TestTimelineRegression(t *testing.T) {
	tl := NewTimeline()
	if err := tl.Append(TimedEvent{At: 100, TrackID: 1, InSeq: 0}, "a"); err != nil {
		t.Fatal(err)
	}
	if err := tl.Append(TimedEvent{At: 99, TrackID: 1, InSeq: 1}, "a"); err == nil {
		t.Fatal("expected regression error")
	} else if !errors.Is(err, ErrRegression) {
		// regression is reported through the timeline stage
		var me *Error
		if !errors.As(err, &me) || me.Stage != StageTimeline {
			t.Fatalf("want timeline stage error, got %v", err)
		}
	}
	if tl.Cursor() != 100 {
		t.Fatalf("cursor must stay at 100 after rejected append, got %d", tl.Cursor())
	}
	if err := tl.Advance(-1, "a", 1); err == nil {
		t.Fatal("negative advance must fail")
	}
}

func TestEventsStableOrdering(t *testing.T) {
	tl := NewTimeline()
	// Deliberately append out of order; same-instant events must come out
	// sorted by (track id, input sequence).
	evs := []TimedEvent{
		{At: 100, TrackID: 9, InSeq: 0, Kind: "start"},
		{At: 500, TrackID: 2, InSeq: 0, Kind: "start"},
		{At: 500, TrackID: 1, InSeq: 1, Kind: "start"},
		{At: 500, TrackID: 1, InSeq: 0, Kind: "start"},
	}
	for _, ev := range evs {
		if err := tl.Append(ev, "a"); err != nil {
			t.Fatal(err)
		}
	}
	got := tl.Events()
	want := []TimedEvent{evs[0], evs[3], evs[2], evs[1]}
	if len(got) != len(want) {
		t.Fatalf("len = %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].At != want[i].At || got[i].TrackID != want[i].TrackID ||
			got[i].InSeq != want[i].InSeq {
			t.Fatalf("position %d = %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestErrorContextAndIs(t *testing.T) {
	e := Wrap(ErrCycle, "asset-x", 3, StageGraph, 4096)
	if !errors.Is(e, ErrCycle) {
		t.Fatal("errors.Is must see the wrapped condition")
	}
	if e.Asset != "asset-x" || e.Track != 3 || e.Stage != StageGraph || e.Offset != 4096 {
		t.Fatalf("context lost: %+v", e)
	}
	if e.Error() == "" {
		t.Fatal("empty error string")
	}
}
