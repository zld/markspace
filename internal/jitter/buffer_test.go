package jitter

import (
	"errors"
	"testing"

	mpierr "mediapipeline/internal/errors"
	"mediapipeline/internal/rtp"
)

func frame(seq uint64, off int64) *rtp.Frame {
	return &rtp.Frame{ExtSeq: seq, StreamOffset: off, Samples: 960}
}

func drainAll(b *Buffer) []Event {
	b.Drain()
	var out []Event
	for {
		ev, ok := b.Release()
		if !ok {
			return out
		}
		out = append(out, ev)
	}
}

func TestInOrderRelease(t *testing.T) {
	b, _ := New(Config{Window: 4, Node: "jitter"})
	for s := uint64(10); s < 13; s++ {
		if err := b.Push(frame(s, 0)); err != nil {
			t.Fatalf("push %d: %v", s, err)
		}
	}
	events := drainAll(b)
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	for i, ev := range events {
		if ev.Frame == nil || ev.Frame.ExtSeq != 10+uint64(i) {
			t.Fatalf("event %d = %+v", i, ev)
		}
	}
}

func TestReorderInsideWindow(t *testing.T) {
	b, _ := New(Config{Window: 3, Node: "jitter"})
	// Arrival order: 0, 2, 1. Window of 3 keeps everything contiguous.
	_ = b.Push(frame(0, 0))
	_ = b.Push(frame(2, 20))
	_ = b.Push(frame(1, 10))
	events := drainAll(b)
	want := []uint64{0, 1, 2}
	for i, ev := range events {
		if ev.Frame == nil || ev.Frame.ExtSeq != want[i] {
			t.Fatalf("event %d = %+v, want seq %d", i, ev, want[i])
		}
	}
}

func TestGapEmittedWhenWindowExhausted(t *testing.T) {
	b, _ := New(Config{Window: 2, Node: "jitter"})
	_ = b.Push(frame(0, 0))
	// With window 2, pushing 1 and 2 while 1 is missing... instead push
	// frames 2 then 3 to exhaust the window around missing 1.
	_ = b.Push(frame(2, 20))
	if _, ok := b.Release(); !ok {
		t.Fatal("seq 0 should release immediately")
	}
	if _, ok := b.Release(); ok {
		t.Fatal("gap must wait until the window is exhausted")
	}
	_ = b.Push(frame(3, 30))
	ev, ok := b.Release()
	if !ok || ev.Gap == nil || ev.Gap.Seq != 1 {
		t.Fatalf("expected gap at seq 1, got %+v ok=%v", ev, ok)
	}
}

func TestLatePacketOutsideWindow(t *testing.T) {
	b, _ := New(Config{Window: 2, Node: "jitter"})
	_ = b.Push(frame(0, 0))
	_ = b.Push(frame(1, 10))
	_, _ = b.Release() // release 0
	_, _ = b.Release() // release 1; ready is now 2
	err := b.Push(frame(0, 0))
	var me *mpierr.MediaError
	if !errors.As(err, &me) || me.Category != mpierr.CatLate {
		t.Fatalf("err = %v, want CatLate", err)
	}
	if me.ByteOffset != 0 {
		t.Fatalf("late offset = %d, want 0", me.ByteOffset)
	}
}

func TestDuplicateNotRetimed(t *testing.T) {
	b, _ := New(Config{Window: 4, Node: "jitter"})
	_ = b.Push(frame(5, 0))
	err := b.Push(frame(5, 0))
	var me *mpierr.MediaError
	if !errors.As(err, &me) || me.Category != mpierr.CatDuplicate {
		t.Fatalf("err = %v, want CatDuplicate", err)
	}
	events := drainAll(b)
	if len(events) != 1 || events[0].Frame.ExtSeq != 5 {
		t.Fatalf("duplicate must not be released twice: %+v", events)
	}
}

func TestDrainEmitsTrailingGaps(t *testing.T) {
	b, _ := New(Config{Window: 4, Node: "jitter"})
	_ = b.Push(frame(0, 0))
	_ = b.Push(frame(2, 20)) // missing 1, no further packets to exhaust window
	events := drainAll(b)
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3 (frame, gap, frame)", len(events))
	}
	if events[0].Frame == nil || events[1].Gap == nil || events[1].Gap.Seq != 1 || events[2].Frame == nil {
		t.Fatalf("unexpected event sequence: %+v", events)
	}
}

func TestWindowFullRejected(t *testing.T) {
	b, _ := New(Config{Window: 2, Node: "jitter"})
	_ = b.Push(frame(0, 0))
	_ = b.Push(frame(1, 10))
	err := b.Push(frame(2, 20))
	var me *mpierr.MediaError
	if !errors.As(err, &me) || me.Category != mpierr.CatLimit {
		t.Fatalf("err = %v, want CatLimit", err)
	}
}

func TestInvalidWindow(t *testing.T) {
	if _, err := New(Config{Window: 0}); err == nil {
		t.Fatal("zero window must be rejected")
	}
}
