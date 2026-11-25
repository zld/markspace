package timeline

import "testing"

func TestConvertTicksKeepsRemainder(t *testing.T) {
	// One 44.1 kHz audio frame expressed at 48 kHz is 48000/44100 ticks.
	c := ConvertTicks(1, 44100, 48000)
	if c.Whole != 1 || c.Remainder != 3900 {
		t.Fatalf("unexpected conversion: %+v", c)
	}
	if got := c.Exact().String(); got != "160/147" {
		t.Fatalf("exact conversion = %s", got)
	}

	// Bulk conversion retains the complete remainder instead of accumulating
	// independently rounded per-sample conversions.
	exact := ConvertTicks(44100, 44100, 48000)
	if exact.Whole != 48000 || exact.Remainder != 0 {
		t.Fatalf("bulk conversion = %+v", exact)
	}
}

func TestClockPreservesNonzeroOrigin(t *testing.T) {
	c := Clock{Timescale: 90000, StartTicks: 180000}
	if got := c.Seconds(180000).String(); got != "2" {
		t.Fatalf("source seconds = %s", got)
	}
	if got := c.NormalizedSeconds(270000).String(); got != "1" {
		t.Fatalf("normalized = %s", got)
	}
}
