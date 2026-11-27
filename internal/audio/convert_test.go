package audio

import (
	"encoding/binary"
	"testing"

	merr "mediatool/internal/errors"
)

// tone builds `frames` interleaved stereo PCM16 samples at amplitude amp.
func tone(frames int, amp int16) []byte {
	buf := make([]byte, frames*2*2)
	for f := 0; f < frames; f++ {
		binary.LittleEndian.PutUint16(buf[4*f:], uint16(amp))
		binary.LittleEndian.PutUint16(buf[4*f+2:], uint16(-amp))
	}
	return buf
}

func TestStereoToMonoAndBack(t *testing.T) {
	c, err := New(Config{
		SourceRate: 48000, SourceLayout: LayoutStereo,
		TargetRate: 48000, TargetLayout: LayoutMono,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Convert(tone(100, 1000))
	if err != nil {
		t.Fatal(err)
	}
	samples := bytesToInt16(out)
	if len(samples) != 100 {
		t.Fatalf("mono frames = %d, want 100", len(samples))
	}
	// (1000 + -1000)/2 = 0
	for i, s := range samples {
		if s != 0 {
			t.Fatalf("mono sample %d = %d, want 0", i, s)
		}
	}

	c2, _ := New(Config{
		SourceRate: 48000, SourceLayout: LayoutMono,
		TargetRate: 48000, TargetLayout: LayoutStereo,
	})
	mono := int16ToBytes([]int16{500, 500})
	out2, err := c2.Convert(mono)
	if err != nil {
		t.Fatal(err)
	}
	s2 := bytesToInt16(out2)
	if len(s2) != 4 || s2[0] != 500 || s2[1] != 500 || s2[2] != 500 || s2[3] != 500 {
		t.Fatalf("mono->stereo duplication wrong: %v", s2)
	}
}

func TestResampleRatio(t *testing.T) {
	c, err := New(Config{
		SourceRate: 48000, SourceLayout: LayoutMono,
		TargetRate: 16000, TargetLayout: LayoutMono,
	})
	if err != nil {
		t.Fatal(err)
	}
	frames := 4800 // 100 ms at 48k
	in := make([]byte, frames*2)
	for f := 0; f < frames; f++ {
		binary.LittleEndian.PutUint16(in[2*f:], uint16(int16(1000+f%7)))
	}
	out, err := c.Convert(in)
	if err != nil {
		t.Fatal(err)
	}
	gotFrames := len(bytesToInt16(out))
	wantFrames := frames * 16000 / 48000
	if gotFrames < wantFrames-2 || gotFrames > wantFrames+2 {
		t.Fatalf("resampled frames = %d, want ~%d", gotFrames, wantFrames)
	}
	if c.Stats().FramesOut != uint64(gotFrames) {
		t.Fatal("frame stats mismatch")
	}
}

func TestUnderrunAndUndecodable(t *testing.T) {
	c, _ := New(Config{
		SourceRate: 48000, SourceLayout: LayoutMono,
		TargetRate: 48000, TargetLayout: LayoutMono, MinFrames: 50,
	})
	if _, err := c.Convert(toneMono(10, 100)); err == nil ||
		merr.AsCode(err) != merr.CodeInsufficient {
		t.Fatalf("underrun not flagged: %v", err)
	}
	if c.Stats().Underruns != 1 {
		t.Fatal("underrun counter not incremented")
	}
	if _, err := c.Convert([]byte{1, 2, 3}); err == nil ||
		merr.AsCode(err) != merr.CodeUndecodable {
		t.Fatalf("odd byte stream accepted: %v", err)
	}
	if c.Stats().Undecodable == 0 {
		t.Fatal("undecodable counter not incremented")
	}
}

func TestLoudnessNormalizationAndClipping(t *testing.T) {
	c, _ := New(Config{
		SourceRate: 48000, SourceLayout: LayoutStereo,
		TargetRate: 48000, TargetLayout: LayoutStereo, TargetLUFS: -14,
	})
	out, err := c.Convert(tone(2000, 100))
	if err != nil {
		t.Fatal(err)
	}
	s := bytesToInt16(out)
	// Quiet input must be amplified toward the target.
	if maxAbs(s) <= 100 {
		t.Fatalf("normalization did not raise level, peak=%d", maxAbs(s))
	}
}

func TestApplyGainClipsAtRail(t *testing.T) {
	c, _ := New(Config{
		SourceRate: 48000, SourceLayout: LayoutMono,
		TargetRate: 48000, TargetLayout: LayoutMono,
	})
	out := c.applyGain([]int16{20000, -20000}, 2.0)
	if out[0] != 32767 || out[1] != -32768 {
		t.Fatalf("rail saturation wrong: %v", out)
	}
	if c.Stats().Clips != 2 {
		t.Fatalf("clips = %d, want 2", c.Stats().Clips)
	}
}

func TestUnsupportedLayout(t *testing.T) {
	if _, err := New(Config{
		SourceRate: 48000, SourceLayout: Layout("5.1"),
		TargetRate: 48000, TargetLayout: LayoutMono,
	}); err == nil || merr.AsCode(err) != merr.CodeLayoutMismatch {
		t.Fatalf("unsupported layout accepted: %v", err)
	}
}

func toneMono(frames int, amp int16) []byte {
	buf := make([]byte, frames*2)
	for f := 0; f < frames; f++ {
		binary.LittleEndian.PutUint16(buf[2*f:], uint16(amp))
	}
	return buf
}

func maxAbs(s []int16) int16 {
	var m int16
	for _, v := range s {
		if v < 0 {
			v = -v
		}
		if v > m {
			m = v
		}
	}
	return m
}
