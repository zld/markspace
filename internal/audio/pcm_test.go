package audio

import (
	"testing"
)

func TestFromPCM16LE(t *testing.T) {
	// Two stereo frames: (0, +0.5) and (-0.5, 0).
	data := []byte{
		0x00, 0x00, 0x00, 0x40,
		0x00, 0xC0, 0x00, 0x00,
	}
	p, partial, err := FromPCM16LE(data, 48000, LayoutStereo)
	if err != nil {
		t.Fatal(err)
	}
	if partial || p.Frames() != 2 {
		t.Fatalf("shape %d partial=%v", p.Frames(), partial)
	}
	if p.Channels[1][0] != 0.5 || p.Channels[0][1] != -0.5 {
		t.Fatalf("deinterleave wrong: %+v", p.Channels)
	}
	// A trailing partial frame is reported, not folded in.
	_, partial, err = FromPCM16LE(append(data, 0x01), 48000, LayoutStereo)
	if err != nil || !partial {
		t.Fatalf("partial frame not reported: %v", err)
	}
	if _, _, err := FromPCM16LE(data, 0, LayoutStereo); err == nil {
		t.Fatal("invalid rate accepted")
	}
}
