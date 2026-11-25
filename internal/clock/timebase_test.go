package clock

import (
	"errors"
	"testing"

	"mediapack/internal/fault"
)

func TestSupportedRatesAreIntegerAndMonotonic(t *testing.T) {
	rates := []Rate{Rate24, Rate25, Rate2997DF, Rate2997ND, Rate50}
	wantDurations := map[string]Ticks{
		"24":       75000,
		"25":       72000,
		"29.97DF":  60060,
		"29.97NDF": 60060,
		"50":       36000,
	}
	for _, r := range rates {
		if got := r.FrameDuration(); got != wantDurations[r.Name] {
			t.Fatalf("%s duration=%d want %d", r.Name, got, wantDurations[r.Name])
		}
		if got := r.FrameToTicks(2); got != 2*r.FrameDuration() {
			t.Fatalf("%s FrameToTicks not linear", r.Name)
		}
	}
}

func TestParseTimecodeConvertsKnownValues(t *testing.T) {
	tests := []struct {
		rate Rate
		code string
		want Ticks
	}{
		{Rate24, "00:00:01:00", Ticks(TicksPerSecond)},
		{Rate25, "00:00:01:00", Ticks(TicksPerSecond)},
		{Rate50, "00:00:01:00", Ticks(TicksPerSecond)},
		{Rate2997ND, "00:00:01:00", 30 * Rate2997ND.FrameDuration()},
		// At the first whole minute two frame numbers are dropped.
		{Rate2997DF, "00:01:00;02", 1800 * Rate2997DF.FrameDuration()},
	}
	for _, tc := range tests {
		got, err := ParseTimecode("fixture", 17, tc.rate, tc.code)
		if err != nil {
			t.Fatalf("ParseTimecode(%s) error: %v", tc.code, err)
		}
		if got.Ticks != tc.want {
			t.Fatalf("%s %s ticks=%d want %d", tc.rate.Name, tc.code, got.Ticks, tc.want)
		}
	}
}

func TestInvalidDropFrameIsClassifiedAndLocated(t *testing.T) {
	// Frame 0 and 1 do not exist at the first non-decadal minute.
	_, err := ParseTimecode("tc.srt", 100, Rate2997DF, "00:01:00;00")
	if fault.Classify(err) != fault.InvalidDropMarker {
		t.Fatalf("kind=%q want invalid_drop_marker: %v", fault.Classify(err), err)
	}
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Source != "tc.srt" || fe.Offset != 109 {
		t.Fatalf("location=%+v want source tc.srt offset 109", fe)
	}

	_, err = ParseTimecode("tc.srt", 0, Rate25, "00:00:01;00")
	if fault.Classify(err) != fault.InvalidDropMarker {
		t.Fatalf("NDF semicolon kind=%q: %v", fault.Classify(err), err)
	}
}

func TestNegativeAndOutOfRangeTimecodesAreNotClamped(t *testing.T) {
	_, err := ParseTimecode("negative.txt", 3, Rate24, "-00:00:01:00")
	if fault.Classify(err) != fault.NegativeTime {
		t.Fatalf("negative kind=%q: %v", fault.Classify(err), err)
	}
	var fe *fault.Error
	if errors.As(err, &fe) && fe.Offset != 3 {
		t.Fatalf("negative offset=%d want 3", fe.Offset)
	}

	for _, code := range []string{"24:00:00:00", "00:60:00:00", "00:00:60:00", "00:00:00:24"} {
		_, err := ParseTimecode("range.txt", 0, Rate24, code)
		if fault.Classify(err) != fault.OutOfRange {
			t.Fatalf("%s kind=%q want out_of_range", code, fault.Classify(err))
		}
	}
}

func TestParseRateSpellings(t *testing.T) {
	for _, s := range []string{"29.97DF", "29.97NDF", "24", "25", "50"} {
		if _, err := ParseRate(s); err != nil {
			t.Fatalf("ParseRate(%q): %v", s, err)
		}
	}
	if _, err := ParseRate("23.976"); err == nil {
		t.Fatal("unsupported rate unexpectedly accepted")
	}
}
