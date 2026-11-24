package model

import (
	"errors"
	"math"
	"testing"
)

func TestScaleRoundTrip(t *testing.T) {
	cases := []struct {
		ticks int64
		ts    TimeScale
	}{
		{0, 1000},
		{1, 1000},
		{90000, 90000},
		{12345, 48000},
		{1_000_000_000, 1_000_000_000},
		{math.MaxInt32, 90000},
	}
	for _, c := range cases {
		ns, err := ScaleRound(c.ticks, c.ts)
		if err != nil {
			t.Fatalf("ScaleRound(%d,%d): %v", c.ticks, c.ts, err)
		}
		back, err := Unscale(ns, c.ts)
		if err != nil {
			t.Fatalf("Unscale: %v", err)
		}
		if back != c.ticks {
			t.Errorf("round trip ticks %d @%d -> ns %d -> %d", c.ticks, c.ts, ns, back)
		}
	}
}

func TestScaleExact(t *testing.T) {
	ns, err := Scale(2, 1000) // 2 ms exactly
	if err != nil || ns != 2_000_000 {
		t.Fatalf("exact scale = %d, %v", ns, err)
	}
	if _, err := Scale(1, 48000); !errors.Is(err, ErrOverflow) {
		t.Fatalf("inexact exact-scale must be rejected, got %v", err)
	}
	if _, err := Scale(1, 0); err == nil {
		t.Fatal("zero time scale must be rejected")
	}
}

func TestBoundedOverflow(t *testing.T) {
	if _, err := Add(MaxPlayTime, 1); !errors.Is(err, ErrOverflow) {
		t.Fatalf("add overflow = %v", err)
	}
	big := int64(math.MaxInt64 / 2)
	if _, err := ScaleRound(big, 1); !errors.Is(err, ErrOverflow) {
		t.Fatalf("scale overflow = %v", err)
	}
	if _, err := Unscale(MaxPlayTime, 1_000_000_000); !errors.Is(err, ErrOverflow) {
		t.Fatalf("unscale overflow = %v", err)
	}
}

func TestRTPWraparound(t *testing.T) {
	if d := RTPTSDiff(0xFFFFFFF0, 0x00000010); d != 32 {
		t.Fatalf("wrap diff = %d, want 32", d)
	}
	if d := RTPTSDiff(0x00000010, 0xFFFFFFF0); d != -32 {
		t.Fatalf("backward diff = %d, want -32", d)
	}
	if d := RTPSeqDiff(0xFFFE, 0x0002); d != 4 {
		t.Fatalf("seq wrap = %d", d)
	}
}

func TestSourceError(t *testing.T) {
	inner := NewError(KindCorruptBox, SourceLoc{Manifest: "a.mpd", Entry: 2}, "bad styp")
	rewrapped := WrapError(KindInternal, SourceLoc{Manifest: "other"}, inner)
	if rewrapped.Kind != KindCorruptBox {
		t.Fatalf("WrapError must preserve original kind, got %s", rewrapped.Kind)
	}
	if s := inner.Error(); s == "" {
		t.Fatal("error string empty")
	}
}
