package model

import (
	"errors"
	"math"
	"testing"
)

func TestScaleToNanoseconds(t *testing.T) {
	cases := []struct {
		name      string
		value     int64
		timescale uint32
		want      int64
		wantKind  ErrorKind
	}{
		{"exact second", 2, 1, 2_000_000_000, ""},
		{"48k audio sample", 48_000, 48_000, 1_000_000_000, ""},
		{"90k quarter", 22_500, 90_000, 250_000_000, ""},
		{"zero value", 0, 1000, 0, ""},
		{"zero timescale", 5, 0, 0, KindInvalidTimescale},
		{"negative value", -1, 1000, 0, KindNegativeDuration},
		{"overflow hi", math.MaxInt64 / 2, 1, 0, KindOverflow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ScaleToNanoseconds(tc.value, tc.timescale)
			if tc.wantKind != "" {
				if err == nil || Classify(err) != tc.wantKind {
					t.Fatalf("want kind %s, got err=%v", tc.wantKind, err)
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

func TestScaleToNanosecondsIsDeterministic(t *testing.T) {
	first, err := ScaleToNanoseconds(123456, 90000)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		got, err := ScaleToNanoseconds(123456, 90000)
		if err != nil || got != first {
			t.Fatalf("non-deterministic mapping: %d vs %d (err=%v)", got, first, err)
		}
	}
}

func TestScaleDurationRejectsNonPositive(t *testing.T) {
	if _, err := ScaleDuration(0, 1000); Classify(err) != KindNegativeDuration {
		t.Fatalf("zero duration: want %s, got %v", KindNegativeDuration, err)
	}
	if _, err := ScaleDuration(-7, 1000); Classify(err) != KindNegativeDuration {
		t.Fatalf("negative duration: want %s, got %v", KindNegativeDuration, err)
	}
	got, err := ScaleDuration(500, 1000)
	if err != nil || got != 500_000_000 {
		t.Fatalf("half second: got %d, err=%v", got, err)
	}
}

func TestSegmentEnd(t *testing.T) {
	s := SegmentDescriptor{StartNS: 1_000_000, DurationNS: 2_000_000}
	if s.EndNS() != 3_000_000 {
		t.Fatalf("EndNS = %d, want 3000000", s.EndNS())
	}
	if KindDialogue.String() != "dialogue" || KindVideo.String() != "video" {
		t.Fatal("asset kind string mapping changed")
	}
}

func TestMediaErrorClassificationAndLocation(t *testing.T) {
	base := NewError(KindTruncatedMP4, "verify", 42, "box declared %d bytes", 99)
	if Locate(base) != 42 || Classify(base) != KindTruncatedMP4 {
		t.Fatalf("location/classification lost: %+v", base)
	}
	if !errors.Is(base, NewError(KindTruncatedMP4, "other", 0, "x")) {
		t.Fatal("errors.Is must match by kind")
	}
	if errors.Is(base, NewError(KindMalformedMP4, "other", 0, "x")) {
		t.Fatal("different kinds must not match")
	}
	if Locate(errors.New("plain")) != -1 || Classify(errors.New("plain")) != "" {
		t.Fatal("foreign errors must degrade gracefully")
	}
}
