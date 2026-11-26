package clock

import (
	"errors"
	"testing"
)

func TestTimecodeExactTicks(t *testing.T) {
	cases := []struct {
		tc    string
		rate  Rate
		frame int64
		ticks Tick
	}{
		// Every supported rate divides 90000 exactly.
		{"00:00:00:01", Rate24, 1, 3750},
		{"00:00:00:01", Rate25, 1, 3600},
		{"00:00:00:01", Rate50, 1, 1800},
		{"00:00:00:00", Rate2997, 0, 0},
		{"00:01:00:02", Rate2997, 1802, 5_411_406},
		{"00:01:00;02", Rate2997, 1800, 5_405_400}, // DF names the same real frame
		{"01:00:00;00", Rate2997, 107_892, 107_892 * 3003},
		{"00:10:00;00", Rate2997, 17_982, 17_982 * 3003},
		{"02:03:04:05", Rate25, 184_605, 184_605 * 3600},
	}
	for _, c := range cases {
		tc, err := ParseTimecode(c.tc, c.rate)
		if err != nil {
			t.Fatalf("%s/%s: %v", c.tc, c.rate, err)
		}
		if got := tc.TotalFrames(); got != c.frame {
			t.Fatalf("%s/%s frames=%d want %d", c.tc, c.rate, got, c.frame)
		}
		ticks, err := tc.Ticks()
		if err != nil {
			t.Fatalf("%s/%s ticks: %v", c.tc, c.rate, err)
		}
		if ticks != c.ticks {
			t.Fatalf("%s/%s ticks=%d want %d (delta %d)", c.tc, c.rate, ticks, c.ticks, ticks-c.ticks)
		}
		// Converting back through the rational rate must land within 1 tick.
		exact := Tick(c.frame * TickRate * c.rate.Den / c.rate.Num)
		if d := ticks - exact; d < -1 || d > 1 {
			t.Fatalf("%s/%s drift %d ticks", c.tc, c.rate, d)
		}
	}
}

func TestDropFrameRejectsDroppedNumbers(t *testing.T) {
	for _, bad := range []string{"00:01:00;00", "00:01:00;01", "00:02:00;00", "00:59:00;01"} {
		if _, err := ParseTimecode(bad, Rate2997); !errors.Is(err, ErrDroppedFrameUsed) {
			t.Fatalf("%s: err=%v want ErrDroppedFrameUsed", bad, err)
		}
	}
	// Every tenth minute starts at frame 00.
	for _, ok := range []string{"00:10:00;00", "00:20:00;00", "01:00:00;00"} {
		if _, err := ParseTimecode(ok, Rate2997); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
	// Drop-frame is only legal at 29.97.
	if _, err := ParseTimecode("00:00:00;01", Rate25); !errors.Is(err, ErrTimecodeDrop) {
		t.Fatalf("25fps DF: err=%v", err)
	}
}

func TestTimecodeRangeAndSyntax(t *testing.T) {
	bad := []struct {
		tc   string
		rate Rate
	}{
		{"00:00:00:24", Rate24},    // frame == nominal fps
		{"00:60:00:00", Rate25},    // minute range
		{"00:00:60:00", Rate25},    // second range
		{"aa:00:00:00", Rate25},    // non-digit
		{"00:00:00", Rate25},       // too few parts
		{"00:00:00:00:00", Rate25}, // too many
	}
	for _, c := range bad {
		if _, err := ParseTimecode(c.tc, c.rate); err == nil {
			t.Fatalf("%s/%s unexpectedly accepted", c.tc, c.rate)
		}
	}
}

func TestSRTAndVTTTime(t *testing.T) {
	got, err := ParseSRTTime("01:02:03,004")
	if err != nil || got != Tick(((1*3600+2*60+3)*1000+4)*(TickRate/1000)) {
		t.Fatalf("srt: %v %d", err, got)
	}
	if _, err := ParseSRTTime("01:02:03.004"); err == nil {
		t.Fatal("srt dot separator accepted")
	}
	if _, err := ParseSRTTime("00:00:00,1000"); err == nil {
		t.Fatal("srt millis range accepted")
	}
	if _, err := ParseSRTTime("00:00:0,001"); err == nil {
		t.Fatal("srt short field accepted")
	}
	v, err := ParseVTTTime("02:03.500")
	if err != nil || v != Tick((2*60+3)*1000*(TickRate/1000)+500*(TickRate/1000)) {
		t.Fatalf("vtt mm:ss: %v %d", err, v)
	}
	v2, err := ParseVTTTime("01:02:03.004")
	if err != nil || v2 != got {
		t.Fatalf("vtt hh:mm:ss: %v %d", err, v2)
	}
	if _, err := ParseVTTTime("1:02:03.004"); err == nil {
		t.Fatal("vtt unpadded accepted")
	}
}

func TestTickStringAndMillis(t *testing.T) {
	if s := FromMilliseconds(3723004).String(); s != "01:02:03.004" {
		t.Fatalf("string=%s", s)
	}
	if ms := FromMilliseconds(3723004).Milliseconds(); ms != 3723004 {
		t.Fatalf("ms=%d", ms)
	}
	if d := Tick(-2 * TickRate); d.String() != "-00:00:02.000" {
		t.Fatalf("neg=%s", d.String())
	}
}
