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
	// Drive the release point forward; window exhausts gaps at seq 1 and 3.
	_ = b.Push(frame(0, 0))
	_ = b.Push(frame(2, 20)) // gap at 1 once the window is exhausted
	_ = b.Push(frame(4, 40)) // forces gap at 1; ready advances toward 3
	// Drain everything so the release point moves past all known sequences.
	b.Drain()
	for {
		if _, ok := b.Release(); !ok {
			break
		}
	}
	// A never-seen sequence below the release point is a genuine late packet.
	err := b.Push(frame(1, 7))
	var me *mpierr.MediaError
	if !errors.As(err, &me) || me.Category != mpierr.CatLate {
		t.Fatalf("err = %v, want CatLate", err)
	}
	if me.ByteOffset != 7 {
		t.Fatalf("late offset = %d, want 7", me.ByteOffset)
	}
}

func TestDuplicateAfterReleaseStillDuplicate(t *testing.T) {
	b, _ := New(Config{Window: 2, Node: "jitter"})
	_ = b.Push(frame(0, 0))
	b.Drain()
	_, _ = b.Release() // seq 0 consumed; release point is now 1
	err := b.Push(frame(0, 0))
	var me *mpierr.MediaError
	if !errors.As(err, &me) || me.Category != mpierr.CatDuplicate {
		t.Fatalf("err = %v, want CatDuplicate", err)
	}
}

func TestDuplicateNotRetimed(t *testing.T) {
	b, _ := New(Config{Window: 4, Node: "jitter"})
	// Two contiguous frames are released into the output queue but stay
	// unconsumed; a repeated in-window sequence is still CatDuplicate.
	_ = b.Push(frame(5, 0))
	_ = b.Push(frame(6, 6))
	// Re-inject seq 5 while the buffer still tracks it as a released-but-known
	// position. It must be classified as a duplicate, not timed again.
	err := b.Push(frame(5, 0))
	var me *mpierr.MediaError
	if !errors.As(err, &me) || me.Category != mpierr.CatDuplicate {
		t.Fatalf("err = %v, want CatDuplicate", err)
	}
	b.Drain()
	events := drainAll(b)
	count5 := 0
	for _, ev := range events {
		if ev.Frame != nil && ev.Frame.ExtSeq == 5 {
			count5++
		}
	}
	if count5 != 1 {
		t.Fatalf("seq 5 released %d times, want exactly 1: %+v", count5, events)
	}
}

func TestStreamStaysBoundedAndEmitsGaps(t *testing.T) {
	// Window 2: a stream with one missing packet must self-drain so the
	// in-memory slot count never exceeds the window, even for long streams.
	b, _ := New(Config{Window: 2, Node: "jitter"})
	seqs := []uint64{0, 2, 3, 5, 6, 7, 8} // missing 1 and 4
	for _, s := range seqs {
		if err := b.Push(frame(s, int64(s))); err != nil {
			t.Fatalf("push %d: %v", s, err)
		}
		if b.Buffered() > 2 {
			t.Fatalf("buffered %d exceeds window 2", b.Buffered())
		}
	}
	b.Drain()
	events := drainAll(b)
	var gotSeqs []uint64
	var gaps []uint64
	for _, ev := range events {
		if ev.Frame != nil {
			gotSeqs = append(gotSeqs, ev.Frame.ExtSeq)
		} else {
			gaps = append(gaps, ev.Gap.Seq)
		}
	}
	if len(gaps) != 2 || gaps[0] != 1 || gaps[1] != 4 {
		t.Fatalf("gaps = %v, want [1 4]", gaps)
	}
	if len(gotSeqs) != len(seqs) {
		t.Fatalf("frame count = %d, want %d", len(gotSeqs), len(seqs))
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

func TestInvalidWindow(t *testing.T) {
	if _, err := New(Config{Window: 0}); err == nil {
		t.Fatal("zero window must be rejected")
	}
}
