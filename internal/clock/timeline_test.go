package clock

import "testing"

func TestConvertRemainderAccumulates(t *testing.T) {
	// 3000 ticks @ 90000 -> 1000 ticks @ 30000 each.
	v, rem, err := Convert(3000, 90000, 30000)
	if err != nil || v != 1000 || rem != 0 {
		t.Fatalf("Convert = (%d,%d,%v), want (1000,0,nil)", v, rem, err)
	}
	// 1 tick @ 3 -> 0 ticks @ 2 with remainder 2 of 3.
	v, rem, _ = Convert(1, 3, 2)
	if v != 0 || rem != 2 {
		t.Fatalf("Convert = (%d,%d), want (0,2)", v, rem)
	}
	// Three such pieces must total exactly 2, matching the whole 3 -> 2.
	acc, _ := NewAccumulator(3, 2)
	total := int64(0)
	for i := 0; i < 3; i++ {
		total, err = acc.Add(1)
		if err != nil {
			t.Fatal(err)
		}
	}
	if total != 2 || acc.Remainder() != 0 {
		t.Fatalf("accumulated = %d rem %d, want 2/0", total, acc.Remainder())
	}
}

func TestConvertRejectsBadScale(t *testing.T) {
	if _, _, err := Convert(1, 0, 10); err != ErrBadScale {
		t.Fatalf("zero scale err = %v, want ErrBadScale", err)
	}
	if _, err := NewAccumulator(1, MaxTimeScale+1); err != ErrBadScale {
		t.Fatalf("overflow scale err = %v, want ErrBadScale", err)
	}
}

func TestWithinTolerance20ms(t *testing.T) {
	// 90 kHz vs 48 kHz boundaries 10 ms apart -> inside 20 ms window.
	ok, err := WithinTolerance(900, 90000, 480, 48000, 20)
	if err != nil || !ok {
		t.Fatalf("10ms gap: ok=%v err=%v, want true", ok, err)
	}
	// 30 ms apart -> outside.
	ok, _ = WithinTolerance(2700, 90000, 0, 48000, 20)
	if ok {
		t.Fatal("30ms gap must exceed 20ms tolerance")
	}
	// Exactly 20 ms is accepted (<=).
	ok, _ = WithinTolerance(1800, 90000, 0, 48000, 20)
	if !ok {
		t.Fatal("exact 20ms boundary must be accepted")
	}
}

func TestRTPUnwrapperWraps(t *testing.T) {
	var u RTPUnwrapper
	cases := []struct {
		in   uint32
		want uint64
	}{
		{0xFFFFFFF0, 0xFFFFFFF0},
		{0x00000005, 0x100000005}, // wrapped once
		{0x0000000A, 0x10000000A},
		{0x00000000, 0x100000000}, // slight backwards inside same cycle stays monotonic-ish
	}
	for _, c := range cases {
		if got := u.Extend(c.in); got != c.want {
			t.Fatalf("Extend(%#x) = %#x, want %#x", c.in, got, c.want)
		}
	}
	if err := CheckMonotonic(5, 4); err == nil {
		t.Fatal("regression must be reported")
	}
}

func TestTimelineDiscontinuityAdvances(t *testing.T) {
	tl, _ := NewTimeline(90000)
	r1, err := tl.Append(1, 3000, 30000) // 3000 @30k = 9000 @90k
	if err != nil {
		t.Fatal(err)
	}
	if r1 != (Range{0, 9000}) {
		t.Fatalf("r1 = %+v", r1)
	}
	// A discontinuity is just the next append on the same timeline.
	r2, _ := tl.Append(1, 1500, 30000)
	if r2 != (Range{9000, 13500}) {
		t.Fatalf("r2 = %+v, want [9000,13500)", r2)
	}
	if tl.Position(1) != 13500 {
		t.Fatalf("position = %d", tl.Position(1))
	}
}

func TestTimelineRejectsTimescaleChange(t *testing.T) {
	tl, _ := NewTimeline(90000)
	if _, err := tl.Append(7, 100, 30000); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.Append(7, 100, 48000); err == nil {
		t.Fatal("mid-track timescale change must be rejected")
	}
}
