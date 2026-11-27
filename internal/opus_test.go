package rtpreplay

import (
	"testing"
)

func TestOpusDurationTable(t *testing.T) {
	cases := []struct {
		config uint8
		frames uint8
		want   uint64
	}{
		{0, 0, 480}, {3, 0, 2880}, {12, 1, 960}, {15, 0, 960},
		{16, 0, 120}, {19, 2, 1920}, {31, 3, 2880}, {8, 1, 960},
	}
	for _, c := range cases {
		b := c.config<<3 | c.frames
		toc, err := ParseOpusTOC([]byte{b})
		if err != nil {
			t.Fatalf("config=%d: %v", c.config, err)
		}
		got, err := toc.DurationTicks(48000)
		if err != nil {
			t.Fatalf("config=%d: %v", c.config, err)
		}
		if got != c.want {
			t.Fatalf("config=%d code=%d: got %d want %d", c.config, c.frames, got, c.want)
		}
	}
}

func TestOpusDurationNon48kClock(t *testing.T) {
	// 960 samples at 48k equals 882 ticks at 44.1k, exactly.
	toc, _ := ParseOpusTOC([]byte{byte(1 << 3)})
	got, err := toc.DurationTicks(44100)
	if err != nil || got != 882 {
		t.Fatalf("got %d,%v want 882", got, err)
	}
	// A 2.5ms frame is 110.25 ticks at 44.1k: not integral, must error.
	toc, _ = ParseOpusTOC([]byte{byte(16 << 3)})
	if _, err := toc.DurationTicks(44100); err == nil {
		t.Fatal("expected non-integral duration error")
	}
}

func TestOpusEmptyPayload(t *testing.T) {
	_, err := ParseOpusTOC(nil)
	if err == nil || AsError(err).Code != CodeDuration {
		t.Fatalf("want CodeDuration, got %v", err)
	}
}

func TestOpusStereoBitAndFrameCode(t *testing.T) {
	toc, err := ParseOpusTOC([]byte{byte(9<<3) | 0x04 | 0x03})
	if err != nil {
		t.Fatal(err)
	}
	if !toc.Stereo || toc.Frames != 3 || toc.FrameSamples != 960 {
		t.Fatalf("toc=%+v", toc)
	}
}
