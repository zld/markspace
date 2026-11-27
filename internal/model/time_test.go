package model

import (
	"errors"
	"math"
	"testing"
)

func TestToTicks(t *testing.T) {
	cases := []struct {
		name      string
		trackTime int64
		scale     uint32
		want      Ticks
		wantClass ErrorClass
	}{
		{"exact second", 1, 1, Nano, ""},
		{"millis at 1000hz", 250, 1000, 250 * Milli, ""},
		{"integer truncation is explicit", 1, 3, Ticks(333333333), ""},
		{"zero timescale", 10, 0, 0, ClassInvalidTimebase},
		{"negative time", -5, 90000, 0, ClassOutOfRange},
		{"overflow", math.MaxInt64 / 2, 1, 0, ClassOutOfRange},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ToTicks(tc.trackTime, tc.scale)
			if tc.wantClass != "" {
				me := AsMediaError(err)
				if me == nil || me.Class != tc.wantClass {
					t.Fatalf("want class %s, got %v", tc.wantClass, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestLimitsValidate(t *testing.T) {
	if err := DefaultLimits().Validate(); err != nil {
		t.Fatalf("defaults must be valid: %v", err)
	}
	bad := []Limits{
		{0, 10, 10},
		{MaxFileBytes + 1, 10, 10},
		{1024, 0, 10},
		{1024, MaxReorderWindow + 1, 10},
		{1024, 10, MaxTrackSamples + 1},
	}
	for i, l := range bad {
		err := l.Validate()
		if AsMediaError(err) == nil || AsMediaError(err).Class != ClassLimitExceeded {
			t.Fatalf("case %d: want limit error, got %v", i, err)
		}
	}
}

func TestMediaErrorContext(t *testing.T) {
	err := AtError(ClassTimeRewind, "a.mov", 3, 4096, "pts went backwards")
	me := AsMediaError(err)
	if me == nil || me.Track != 3 || me.Offset != 4096 || me.Class != ClassTimeRewind {
		t.Fatalf("context lost: %+v", me)
	}
	wrapped := errors.New("wrapper: " + err.Error())
	if AsMediaError(wrapped) != nil {
		t.Fatal("plain string wrapping must not fake a MediaError")
	}
	if !errors.Is(err, err) {
		t.Fatal("identity check failed")
	}
}
