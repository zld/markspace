package timecode_test

import (
	"testing"

	"archivecheck/internal/fault"
	"archivecheck/internal/timebase"
	"archivecheck/internal/timecode"
)

func TestParseAtRates(t *testing.T) {
	cases := []struct {
		text   string
		rate   timebase.Rate
		frames int64
		nanos  timebase.Nanos
	}{
		{"00:00:00:00", timebase.FPS24, 0, 0},
		{"00:00:01:00", timebase.FPS24, 24, 1000000000},
		{"01:02:03:04", timebase.FPS25, 93079, 3723160000000},
		{"00:00:01:49", timebase.FPS50, 99, 1980000000},
		{"00:00:00:29", timebase.FPS2997, 29, 29 * 33366667},
		{"00:10:00;00", timebase.FPS2997, 17982, 0},
		{"00:10:00:00", timebase.FPS2997, 18000, 0},
	}
	for _, c := range cases {
		v, err := timecode.Parse(c.text, c.rate)
		if err != nil {
			t.Errorf("%s @ %v: %v", c.text, c.rate, err)
			continue
		}
		if v.Frames != c.frames {
			t.Errorf("%s frames = %d, want %d", c.text, v.Frames, c.frames)
		}
		if c.nanos != 0 || c.frames == 0 {
			got, err := c.rate.TimecodeToNanos(v.Frames)
			if err != nil || got != v.Nanos {
				t.Errorf("%s nanos = %d (want %d), %v", c.text, v.Nanos, got, err)
			}
		}
	}
	v, _ := timecode.Parse("00:10:00;00", timebase.FPS2997)
	if v.Nanos != 599999400000 {
		t.Errorf("DF nanos = %d, want 599999400000", v.Nanos)
	}
	// NDF 00:10:00:00 labels 18000 frames = 600.6 s at 29.97.
	ndf, _ := timecode.Parse("00:10:00:00", timebase.FPS2997)
	if ndf.Nanos != 600600000000 {
		t.Errorf("NDF nanos = %d, want 600600000000", ndf.Nanos)
	}
	// DF 00:10:00;00 labels 17982 real frames = 599.9994 s.
	df, _ := timecode.Parse("00:10:00;00", timebase.FPS2997)
	if df.Nanos != 599999400000 {
		t.Errorf("DF 10min nanos = %d, want 599999400000", df.Nanos)
	}
}

func TestParseDropFrameInvalidLabel(t *testing.T) {
	// 00:01:00;00 and 00:01:00;01 are dropped labels at 29.97 DF.
	for _, text := range []string{"00:01:00;00", "00:01:00;01"} {
		_, err := timecode.Parse(text, timebase.FPS2997)
		fe := fault.As(err)
		if fe == nil || fe.Stage != fault.StageTimecode || fe.Kind != fault.KindInvalid {
			t.Fatalf("%s: expected timecode/invalid, got %v", text, err)
		}
		if fe.Offset != 6 {
			t.Errorf("%s: offset = %d, want 6", text, fe.Offset)
		}
	}
	// 00:10:00;00 is a legal DF label.
	if _, err := timecode.Parse("00:10:00;00", timebase.FPS2997); err != nil {
		t.Errorf("legal DF label rejected: %v", err)
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	cases := []struct {
		text string
		rate timebase.Rate
		kind string
		off  int64
	}{
		{"-00:00:01:00", timebase.FPS25, fault.KindInvalid, 0},
		{"00:60:00:00", timebase.FPS25, fault.KindInvalid, 3},
		{"00:00:60:00", timebase.FPS25, fault.KindInvalid, 6},
		{"00:00:00:25", timebase.FPS25, fault.KindInvalid, 9},
		{"00:00:01;00", timebase.FPS25, fault.KindInvalid, 8},
		{"00:00:01:0", timebase.FPS25, fault.KindTruncated, 0},
		{"00:00:01:0X", timebase.FPS25, fault.KindInvalid, 9},
		{"99:59:59:00", timebase.FPS50, fault.KindOverflow, 0},
		{"00:00:01:00extra", timebase.FPS25, fault.KindInvalid, 11},
		{"", timebase.FPS25, fault.KindInvalid, 0},
	}
	for _, c := range cases {
		_, err := timecode.ParseAt(c.text, c.rate, 0)
		fe := fault.As(err)
		if fe == nil {
			t.Errorf("%q: no offset error: %v", c.text, err)
			continue
		}
		if fe.Kind != c.kind || fe.Offset != c.off {
			t.Errorf("%q: got %s at %d, want %s at %d (%v)", c.text, fe.Kind, fe.Offset, c.kind, c.off, err)
		}
	}
}

func TestParseAtRebasesOffset(t *testing.T) {
	_, err := timecode.ParseAt("00:00:60:00", timebase.FPS25, 100)
	if got := fault.OffsetOf(err); got != 106 {
		t.Errorf("offset = %d, want 106", got)
	}
}

func TestFormatNanos(t *testing.T) {
	v, err := timecode.Parse("01:02:03:04", timebase.FPS25)
	if err != nil {
		t.Fatal(err)
	}
	got, err := timecode.FormatNanos(v.Nanos, timebase.FPS25)
	if err != nil {
		t.Fatal(err)
	}
	if got != "01:02:03:04" {
		t.Errorf("FormatNanos = %q", got)
	}
}
