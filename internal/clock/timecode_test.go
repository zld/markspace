package clock

import (
	"testing"

	"mediamerge/internal/fault"
)

func TestParseNDFRates(t *testing.T) {
	cases := []struct {
		s      string
		r      Rate
		frames int64
	}{
		{"00:00:01:00", Rate24, 24},
		{"00:01:00:00", Rate25, 1500},
		{"00:00:01:00", Rate2997, 30},
		{"01:00:00:00", Rate2997, 108000},
	}
	for _, c := range cases {
		got, df, err := ParseTimecode(c.s, c.r)
		if err != nil || df || got != c.frames {
			t.Errorf("ParseTimecode(%q) = %d df=%v err=%v, want %d", c.s, got, df, err, c.frames)
		}
	}
}

func TestParseDropFrame(t *testing.T) {
	// Known SMPTE 30DF frame numbers.
	cases := []struct {
		s      string
		frames int64
	}{
		{"00:00:01;00", 30},
		{"00:01:00;02", 1800},   // nominal 1802 minus the 2 frames skipped at minute 1
		{"00:10:00;00", 17982},  // 18000 nominal minus 9*2 skipped frames
		{"01:00:00;00", 107892}, // 108000 nominal minus 108 skipped frames
	}
	for _, c := range cases {
		got, df, err := ParseTimecode(c.s, Rate2997DF)
		if err != nil || !df || got != c.frames {
			t.Errorf("ParseTimecode(%q) = %d df=%v err=%v, want %d", c.s, got, df, err, c.frames)
		}
	}
}

func TestDropFrameLabelRejectedForOtherRates(t *testing.T) {
	for _, r := range []Rate{Rate24, Rate25} {
		_, _, err := ParseTimecode("00:00:01;00", r)
		if err == nil || fault.As(err).Kind != fault.KindDropFrame {
			t.Fatalf("rate %d/%d must reject DF label, got %v", r.Num, r.Den, err)
		}
	}
}

func TestDroppedFrameNumberNotSilentlyFixed(t *testing.T) {
	// Frames 0 and 1 at minute 1 do not exist in drop-frame timecode.
	for _, ff := range []string{"00:01:00;00", "00:01:00;01"} {
		_, _, err := ParseTimecode(ff, Rate2997DF)
		if err == nil || fault.As(err).Kind != fault.KindTimecode {
			t.Fatalf("%s names a skipped frame and must be rejected, got %v", ff, err)
		}
	}
	// Minute 10 keeps all frames.
	if _, _, err := ParseTimecode("00:10:00;00", Rate2997DF); err != nil {
		t.Fatalf("tenth minute has no dropped frames: %v", err)
	}
}

func TestTimecodeFieldValidation(t *testing.T) {
	bad := []string{
		"00:00:10",       // too few fields
		"00:00:10:00:00", // too many
		"00:60:00:00",    // minute overflow
		"00:00:60:00",    // second overflow
		"0a:00:00:00",    // non-numeric
		"00.00.10.00",    // decimal separator
	}
	for _, s := range bad {
		if _, _, err := ParseTimecode(s, Rate25); err == nil {
			t.Errorf("%q must be rejected", s)
		}
	}
	_, _, err := ParseTimecode("00:00:00:25", Rate25)
	if err == nil || fault.As(err).Kind != fault.KindTimecode {
		t.Fatalf("frame field >= fps must be rejected, got %v", err)
	}
	_, _, err = ParseTimecode("24:00:00:00", Rate25)
	if err == nil || fault.As(err).Kind != fault.KindOverflow {
		t.Fatalf("24h overflow must be rejected, got %v", err)
	}
}

func TestFrameTickRoundTrip(t *testing.T) {
	for _, r := range []Rate{Rate24, Rate25, Rate2997} {
		for _, f := range []int64{0, 1, 29, 30, 1798, 107892} {
			tk, err := FramesToTicks(f, r)
			if err != nil {
				t.Fatalf("FramesToTicks(%d, %d/%d): %v", f, r.Num, r.Den, err)
			}
			back, err := TicksToFrames(tk, r)
			if err != nil || back != f {
				t.Fatalf("roundtrip frame %d at %d/%d -> %d (err %v)", f, r.Num, r.Den, back, err)
			}
		}
	}
}

func TestFromTimescale(t *testing.T) {
	tk, err := FromTimescale(90000, 90000)
	if err != nil || tk != 90000 {
		t.Fatalf("FromTimescale = %d, %v", tk, err)
	}
	if _, err := FromTimescale(1, 0); err == nil {
		t.Fatal("zero timescale must error")
	}
}
