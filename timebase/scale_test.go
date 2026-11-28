package timebase

import (
	"errors"
	"testing"

	"mediarc/model"
)

func TestParseClock(t *testing.T) {
	cases := []struct {
		text string
		want int64
	}{
		{"00:00:00,000", 0},
		{"00:00:01,001", 1_001_000_000},
		{"00:00:01.5", 1_500_000_000},
		{"01:02:03.004", 3_723_004_000_000},
		{"02:03.250", 123_250_000_000},
		{"7.125", 7_125_000_000},
		{"00:00:00.123456789", 123_456_789},
	}
	for _, tc := range cases {
		got, err := ParseClock(tc.text)
		if err != nil {
			t.Fatalf("ParseClock(%q): %v", tc.text, err)
		}
		if got != tc.want {
			t.Fatalf("ParseClock(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}
	for _, text := range []string{"", "::", "00:61:00,000", "00:00:00,12x", "1:2:3:4", "00:00:00,1234567890"} {
		if _, err := ParseClock(text); !model.IsCode(err, model.CodeInvalidTimecode) {
			t.Fatalf("ParseClock(%q) error = %v, want invalid_timecode", text, err)
		}
	}
}

func TestNanosScaleRoundTrip(t *testing.T) {
	for _, timescale := range []uint32{1000, 90000, 48000, 30000} {
		for _, ns := range []int64{0, 1, 999_999, 1_000_000_001, 3_600_000_000_000} {
			ticks, err := NanosToScale(ns, timescale)
			if err != nil {
				t.Fatalf("NanosToScale(%d, %d): %v", ns, timescale, err)
			}
			back, err := NanosFromScale(ticks, timescale)
			if err != nil {
				t.Fatalf("NanosFromScale: %v", err)
			}
			diff := back - ns
			if diff < 0 {
				diff = -diff
			}
			if diff > 1_000_000_000/int64(timescale)+1 {
				t.Fatalf("roundtrip ns=%d timescale=%d -> %d", ns, timescale, back)
			}
		}
	}
	if _, err := NanosFromScale(1, 0); !model.IsCode(err, model.CodeInvalidArgument) {
		t.Fatalf("zero timescale: %v", err)
	}
	if _, err := NanosFromScale(-1500, 1000); !errors.Is(err, nil) {
		t.Fatal(err)
	}
	if got, _ := NanosFromScale(-1500, 1000); got != -1_500_000_000 {
		t.Fatalf("negative scale conversion = %d", got)
	}
}

func TestFormatNanos(t *testing.T) {
	cases := map[int64]string{
		0:                  "00:00:00.000",
		1_001_000_000:      "00:00:01.001",
		3_600_000_000_000:  "01:00:00.000",
		86_399_999_000_000: "23:59:59.999",
	}
	for ns, want := range cases {
		if got := FormatNanos(ns); got != want {
			t.Fatalf("FormatNanos(%d) = %q, want %q", ns, got, want)
		}
	}
}
