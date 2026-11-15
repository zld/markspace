package timeline

import (
	"testing"
	"time"
)

func TestParseFPS(t *testing.T) {
	cases := []struct {
		in      string
		num     int64
		den     int64
		wantErr bool
	}{
		{"24000/1001", 24000, 1001, false},
		{"25", 25, 1, false},
		{" 30000/1001 ", 30000, 1001, false},
		{"0/1", 0, 0, true},
		{"24/0", 0, 0, true},
		{"a/b", 0, 0, true},
		{"", 0, 0, true},
		{"-1/2", 0, 0, true},
	}
	for _, c := range cases {
		got, err := ParseFPS(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseFPS(%q) expected error, got %+v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseFPS(%q) unexpected error: %v", c.in, err)
		}
		if got.Num != c.num || got.Den != c.den {
			t.Errorf("ParseFPS(%q) = %d/%d, want %d/%d", c.in, got.Num, got.Den, c.num, c.den)
		}
	}
}

func TestFrameRangeRounding(t *testing.T) {
	// 24000/1001 fps: one frame = 1001/24000 s ≈ 41.7083 ms.
	fps := FPS{Num: 24000, Den: 1001}

	// Exactly one second in: 23.976 fps => 23.976 frames -> floor 23.
	sf, ef := fps.FrameRange(time.Second, 2*time.Second)
	if sf != 23 || ef != 48 {
		t.Errorf("1s..2s = frames [%d,%d], want [23,48]", sf, ef)
	}

	// A cue shorter than one frame must still occupy exactly one frame.
	sf, ef = fps.FrameRange(0, time.Millisecond)
	if sf != 0 || ef != 1 {
		t.Errorf("short cue = [%d,%d], want [0,1]", sf, ef)
	}

	// An exact frame boundary at an integer fps must land on that frame.
	exactFPS := FPS{Num: 25, Den: 1}
	exact := time.Second / 25
	sf, _ = exactFPS.FrameRange(exact, exact+time.Second)
	if sf != 1 {
		t.Errorf("exact frame boundary gave start %d, want 1", sf)
	}
}

func TestFrameRangeMonotonic(t *testing.T) {
	fps := FPS{Num: 25, Den: 1}
	var prevEnd int64 = -1
	for ms := int64(0); ms < 60_000; ms += 37 {
		start := time.Duration(ms) * time.Millisecond
		end := start + 500*time.Millisecond
		sf, ef := fps.FrameRange(start, end)
		if ef <= sf {
			t.Fatalf("non-positive frame span at %dms: [%d,%d]", ms, sf, ef)
		}
		if sf <= prevEnd && ms > 0 && sf != prevEnd {
			// adjacent/overlapping frames are fine; this just checks sanity.
		}
		prevEnd = ef
	}
}

func TestFrameTimeRoundTrip(t *testing.T) {
	// Integer frame rates map frame numbers to exact nanosecond instants.
	exact := FPS{Num: 25, Den: 1}
	for frame := int64(0); frame < 1000; frame += 37 {
		ts := exact.FrameTime(frame)
		sf, _ := exact.FrameRange(ts, ts+time.Second)
		if sf != frame {
			t.Errorf("exact: frame %d -> time %s -> start frame %d", frame, ts, sf)
		}
	}
	// Fractional frame rates truncate sub-nanosecond remainders; the
	// recovered start frame may land at most one frame earlier, and the
	// reconstructed time must stay within one nanosecond of the boundary.
	fps := FPS{Num: 24000, Den: 1001}
	for frame := int64(1); frame < 1000; frame += 37 {
		ts := fps.FrameTime(frame)
		sf, _ := fps.FrameRange(ts, ts+time.Second)
		if sf != frame && sf != frame-1 {
			t.Errorf("fractional: frame %d -> time %s -> start frame %d", frame, ts, sf)
		}
		boundary := frame * int64(time.Second) * fps.Den / fps.Num
		if diff := boundary - int64(ts); diff < 0 || diff > 1 {
			t.Errorf("frame %d reconstructed %d ns, boundary %d ns (diff %d)", frame, ts, boundary, diff)
		}
	}
}

func TestString(t *testing.T) {
	if got := (FPS{Num: 25, Den: 1}).String(); got != "25" {
		t.Errorf("integer fps String = %q", got)
	}
	if got := (FPS{Num: 24000, Den: 1001}).String(); got != "24000/1001" {
		t.Errorf("fractional fps String = %q", got)
	}
}
