package pipeline

import (
	"testing"
	"time"
)

func TestClockInjection(t *testing.T) {
	c := NewClock(time.Unix(1000, 0))
	if got := c.Now().Unix(); got != 1000 {
		t.Fatalf("now = %d", got)
	}
	c.Advance(75 * time.Second)
	if got := c.Now().Unix(); got != 1075 {
		t.Fatalf("now after advance = %d", got)
	}
	c.Advance(-time.Hour)
	if got := c.Now().Unix(); got != 1075 {
		t.Fatalf("negative advance moved the clock to %d", got)
	}
	c.SetOffset(5 * time.Second)
	if got := c.Offset(); got != 5*time.Second {
		t.Fatalf("offset = %s", got)
	}
}

func TestRotationsFromManifest(t *testing.T) {
	h := newHarness(t, 12, 3)
	rot := RotationsFromManifest(h.pkg.Manifest)
	if len(rot) != 3 {
		t.Fatalf("%d rotations, want 3", len(rot))
	}
	if rot[0].FromKeyID != "k1" || rot[0].ToKeyID != "k2" || rot[0].AtSegment != 3 {
		t.Fatalf("first rotation = %+v", rot[0])
	}
}
