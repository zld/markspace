package clock

import "testing"

func TestRationalRoundTrip(t *testing.T) {
	cases := []Rational{{1, 90000}, {1, 48000}, {1001, 30000}, {1, 25}}
	for _, r := range cases {
		for _, v := range []int64{0, 1, 7, 12345, 1 << 31} {
			tk, err := ScaleToTicks(v, r)
			if err != nil {
				t.Fatalf("ScaleToTicks(%d,%v): %v", v, r, err)
			}
			back, err := TicksToScale(tk, r)
			if err != nil {
				t.Fatalf("TicksToScale: %v", err)
			}
			if back != v {
				t.Fatalf("round trip %d -> %d ns -> %d (r=%v)", v, tk.Ns(), back, r)
			}
		}
	}
}

func TestSnapToGrid(t *testing.T) {
	grid := Ticks(100)
	if got := SnapToGrid(149, grid); got != 100 {
		t.Fatalf("snap 149 -> %d", got)
	}
	if got := SnapToGrid(150, grid); got != 200 {
		t.Fatalf("tie should round up, got %d", got)
	}
	if got := SnapToGrid(-150, grid); got != -100 {
		t.Fatalf("negative snap -> %d", got)
	}
}

func TestTimecodeNDFRoundTrip(t *testing.T) {
	rates := []FrameRate{FrameRate24(), FrameRate25(), FrameRate30(), FrameRate50(), FrameRate2997(false)}
	for _, fr := range rates {
		for _, f := range []int64{0, 1, 59, 1798, 90000} {
			tc, err := FromTotalFrames(f, fr)
			if err != nil {
				t.Fatalf("FromTotalFrames %d %s: %v", f, fr.Name, err)
			}
			back, err := tc.TotalFrames(fr)
			if err != nil {
				t.Fatalf("TotalFrames: %v", err)
			}
			if back != f {
				t.Fatalf("%s frame %d round trip -> %d (%s)", fr.Name, f, back, tc)
			}
		}
	}
}

func TestDropFrameKnownCodes(t *testing.T) {
	fr := FrameRate2997(true)
	// 00:01:00;02 is the first valid code of the first drop minute.
	tc, err := ParseTimecode("00:01:00;02", fr)
	if err != nil {
		t.Fatalf("valid DF code rejected: %v", err)
	}
	f, err := tc.TotalFrames(fr)
	if err != nil {
		t.Fatal(err)
	}
	// One minute at 30fps nominal is 1800 frame counts; the two skipped
	// numbers mean 1798 real frames elapsed, so this code is frame 1800.
	if f != 1800 {
		t.Fatalf("want 1800 at 00:01:00;02, got %d", f)
	}
	back, err := FromTotalFrames(f, fr)
	if err != nil || back.String() != "0:01:00;02" {
		t.Fatalf("DF inverse failed: %+v err=%v", back, err)
	}
}

func TestDropFrameIllegalNumbers(t *testing.T) {
	fr := FrameRate2997(true)
	for _, bad := range []string{"00:01:00;00", "00:01:00;01", "00:09:00;01"} {
		if _, err := ParseTimecode(bad, fr); err == nil {
			t.Fatalf("illegal dropped number %q accepted", bad)
		}
	}
	// Tenth minutes keep frames 0 and 1: legal.
	if _, err := ParseTimecode("00:10:00;00", fr); err != nil {
		t.Fatalf("legal 10-minute code rejected: %v", err)
	}
	// NDF rate must reject the ';' marker and vice versa.
	if _, err := ParseTimecode("00:01:00;02", FrameRate2997(false)); err == nil {
		t.Fatal("NDF rate accepted DF marker")
	}
	if _, err := ParseTimecode("00:01:00:02", FrameRate2997(true)); err == nil {
		t.Fatal("DF rate accepted NDF marker")
	}
}

func TestTimecodeTicksRoundTripWithinOneFrame(t *testing.T) {
	rates := []FrameRate{FrameRate24(), FrameRate25(), FrameRate30(), FrameRate50(),
		FrameRate2997(true), FrameRate2997(false), FrameRate5994(true)}
	for _, fr := range rates {
		grid := fr.FrameDurationNS()
		for f := int64(0); f < 3600; f += 37 {
			tc, err := FromTotalFrames(f, fr)
			if err != nil {
				t.Fatal(err)
			}
			tk, err := tc.ToTicks(fr)
			if err != nil {
				t.Fatal(err)
			}
			back, err := TicksToTimecode(tk, fr)
			if err != nil {
				t.Fatal(err)
			}
			bf, _ := back.TotalFrames(fr)
			if bf != f {
				t.Fatalf("%s: frame %d -> %s -> frame %d", fr.Name, f, tc, bf)
			}
			if diff := Abs(tk - Ticks(int64(f)*int64(grid))); diff > grid {
				t.Fatalf("%s frame %d drift %d > frame %d", fr.Name, f, diff, grid)
			}
		}
	}
}
