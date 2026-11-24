package timeline

import "testing"

func TestClockConversions(t *testing.T) {
	// 30kHz MP4 timescale: 30000 ticks = 1s = MasterHz master ticks.
	c := Clock{Hz: 30000}
	if got := c.ToTicks(30000); got != MasterHz {
		t.Fatalf("1s in master ticks = %d, want %d", got, MasterHz)
	}
	// RTP 90kHz video clock: one frame at 30fps is 3000 RTP ticks.
	rtp := Clock{Hz: 90000}
	if got := rtp.ToTicks(3000); got != MasterHz/30 {
		t.Fatalf("one frame = %d master ticks, want %d", got, MasterHz/30)
	}
	// Degenerate clock must not divide by zero.
	if got := (Clock{}).ToTicks(123); got != 0 {
		t.Fatalf("zero Hz clock returned %d, want 0", got)
	}
}

func TestRationalAndMillis(t *testing.T) {
	// 29.97fps NTSC edit rate 30000/1001: one frame in master ticks.
	one := ToTicksRational(1, 30000, 1001)
	if Millis(one) < 33 || Millis(one) > 34 {
		t.Fatalf("NTSC frame = %dms, want ~33ms", Millis(one))
	}
	if got := Millis(MasterHz / 2); got != 500 {
		t.Fatalf("half second = %dms, want 500", got)
	}
	if got := Millis(-MasterHz / 4); got != -250 {
		t.Fatalf("negative quarter = %dms, want -250", got)
	}
}

func TestWindowMerge(t *testing.T) {
	a := Window{Start: 0, End: 10}
	b := Window{Start: 5, End: 20}
	if m := a.Merge(b); m.Start != 0 || m.End != 20 {
		t.Fatalf("merge = %v, want [0,20)", m)
	}
	if !a.Overlaps(b) || (Window{Start: 20, End: 30}).Overlaps(b) {
		t.Fatal("overlap predicate wrong")
	}
	if (Window{Start: 0, End: 5}).Contains(5) {
		t.Fatal("half-open window must not contain its end")
	}
}

func TestWrappingCounters(t *testing.T) {
	// Forward distance across a 32-bit wrap must stay positive.
	if d := Advance32(0xFFFFFFF0, 0x00000010); d != 32 {
		t.Fatalf("32-bit wrap distance = %d, want 32", d)
	}
	// Small backwards jump stays negative (real reorder), large backwards
	// jump is interpreted as wrap-around forwards.
	if d := Advance32(100, 90); d != -10 {
		t.Fatalf("backwards distance = %d, want -10", d)
	}
	if d := Advance16(0xFFF0, 0x0010); d != 32 {
		t.Fatalf("16-bit wrap distance = %d, want 32", d)
	}
	axis := int64(0)
	axis = Monotonic32(axis, 0xFFFFFFF0, 0x00000010)
	if axis <= 0 {
		t.Fatalf("monotonic axis regressed across wrap: %d", axis)
	}
}
