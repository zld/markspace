package receiver

import (
	"errors"
	"testing"

	"jitterlab/internal/packet"
)

func TestGuardResetsOnValidPacket(t *testing.T) {
	g, err := NewGuard(3)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Observe(packet.ErrChecksum); err != nil {
		t.Fatal(err)
	}
	if err := g.Observe(packet.ErrTruncated); err != nil {
		t.Fatal(err)
	}
	if err := g.Observe(nil); err != nil {
		t.Fatal(err)
	}
	if g.Consecutive() != 0 {
		t.Fatalf("consecutive = %d, want 0 after valid packet", g.Consecutive())
	}
	if err := g.Observe(packet.ErrUnknownVersion); err != nil {
		t.Fatal(err)
	}
	if err := g.Observe(packet.ErrBadLength); err != nil {
		t.Fatal(err)
	}
	err = g.Observe(packet.ErrChecksum)
	if !errors.Is(err, ErrStreamDamaged) {
		t.Fatalf("err = %v, want ErrStreamDamaged", err)
	}
	if g.Consecutive() != 3 || g.Longest() != 3 {
		t.Fatalf("guard state = consec %d longest %d", g.Consecutive(), g.Longest())
	}
	if g.Invalid != 5 {
		t.Fatalf("invalid = %d, want 5", g.Invalid)
	}
}

func TestGuardTracksLongestRun(t *testing.T) {
	g, _ := NewGuard(10)
	for i := 0; i < 4; i++ {
		g.Observe(packet.ErrChecksum)
	}
	g.Observe(nil)
	for i := 0; i < 2; i++ {
		g.Observe(packet.ErrChecksum)
	}
	if g.Longest() != 4 {
		t.Fatalf("longest = %d, want 4", g.Longest())
	}
}

func TestGuardRejectsBadLimit(t *testing.T) {
	if _, err := NewGuard(0); err == nil {
		t.Fatal("accepted zero limit")
	}
	if _, err := NewGuard(-1); err == nil {
		t.Fatal("accepted negative limit")
	}
}
