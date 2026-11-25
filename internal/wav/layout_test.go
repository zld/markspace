package wav

import (
	"bytes"
	"testing"
)

func TestLayoutValidAndCanonical(t *testing.T) {
	l, err := NewLayout(2, 16, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if l.FrameBytes() != 4 || l.SampleBytes() != 2 {
		t.Fatalf("frame bytes wrong: %d", l.FrameBytes())
	}
	if len(l.Order) != 2 || l.Order[0] != "L" || l.Order[1] != "R" {
		t.Fatalf("stereo order wrong: %v", l.Order)
	}
}

func TestLayoutRejectsUnknown(t *testing.T) {
	if _, err := NewLayout(3, 16, 48000); err == nil {
		t.Fatal("3-channel layout should be rejected")
	}
	if _, err := NewLayout(2, 12, 48000); err == nil {
		t.Fatal("12-bit layout should be rejected")
	}
}

func TestLayoutMatches(t *testing.T) {
	a, _ := NewLayout(2, 16, 48000)
	b, _ := NewLayout(2, 16, 48000)
	c, _ := NewLayout(1, 16, 48000)
	if !a.Matches(b) {
		t.Fatal("equal layouts mismatch")
	}
	if a.Matches(c) {
		t.Fatal("different layouts match")
	}
}

func TestLayoutForParsedFile(t *testing.T) {
	raw := buildWAV(44100, 1, 16, 4, "")
	f, err := Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	l, err := LayoutFor(f)
	if err != nil || l.Channels != 1 || l.SampleRate != 44100 {
		t.Fatalf("layout: %+v err=%v", l, err)
	}
}
