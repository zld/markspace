package rtp

import (
	"context"
	"errors"
	"testing"

	"mediapipe/internal/model"
)

func seqOf(ds []Delivered) []uint16 {
	out := make([]uint16, len(ds))
	for i, d := range ds {
		out[i] = d.Sequence
	}
	return out
}

func TestWindowReordersInWindow(t *testing.T) {
	w, err := New(Config{Size: 8})
	if err != nil {
		t.Fatal(err)
	}
	order := []uint16{100, 102, 101, 104, 103}
	var got []uint16
	for _, s := range order {
		out, err := w.Push(Packet{Sequence: s, Timestamp: uint32(s) * 90})
		if err != nil {
			t.Fatalf("push %d: %v", s, err)
		}
		got = append(got, seqOf(out)...)
	}
	got = append(got, seqOf(w.Finalise())...)
	want := []uint16{100, 101, 102, 103, 104}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delivery order = %v, want %v", got, want)
		}
	}
	st := w.Stats()
	if st.Received != 5 || st.Committed != 5 || st.Reordered < 2 {
		t.Fatalf("stats = %+v", st)
	}
	// Two slots (101 and 103) missed their turn and were later filled.
	if st.Unrecoverable != 0 || st.Recovered != 2 || st.LossRecoveryRate() != 1 {
		t.Fatalf("unexpected loss: %+v", st)
	}
}

func TestWindowCountsDuplicates(t *testing.T) {
	w, _ := New(Config{Size: 4})
	for _, s := range []uint16{1, 2, 2, 1, 3} {
		if _, err := w.Push(Packet{Sequence: s}); err != nil {
			t.Fatal(err)
		}
	}
	if w.Stats().Duplicates != 2 {
		t.Fatalf("duplicates = %d", w.Stats().Duplicates)
	}
	if w.Stats().Committed != 3 {
		t.Fatalf("committed = %d", w.Stats().Committed)
	}
}

func TestLatePacketDoesNotRewriteCommitted(t *testing.T) {
	w, _ := New(Config{Size: 4})
	if _, err := w.Push(Packet{Sequence: 10, Payload: []byte("a")}); err != nil {
		t.Fatal(err)
	}
	// 10 was committed; a resent 10 must not be emitted again.
	out, err := w.Push(Packet{Sequence: 10, Payload: []byte("X")})
	if err != nil || len(out) != 0 {
		t.Fatalf("late resend emitted %v (err %v)", seqOf(out), err)
	}
	if w.Stats().Duplicates != 1 {
		t.Fatalf("duplicates = %d", w.Stats().Duplicates)
	}
	out, err = w.Push(Packet{Sequence: 11, Payload: []byte("b")})
	if err != nil || len(out) != 1 || string(out[0].Payload) != "b" {
		t.Fatalf("seq 11 = %v err=%v", seqOf(out), err)
	}
}

func TestLossBeyondWindowIsUnrecoverable(t *testing.T) {
	w, _ := New(Config{Size: 4})
	if _, err := w.Push(Packet{Sequence: 0}); err != nil {
		t.Fatal(err)
	}
	// Jump 10 ahead with a window of 4: slots 1..5 are more than the window
	// behind seq 10, so they are irreparably lost. Slots 6..9 are still
	// within the window and may yet arrive late.
	out, err := w.Push(Packet{Sequence: 10})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected buffered only, got %v", seqOf(out))
	}
	st := w.Stats()
	if st.Unrecoverable != 5 || st.Gaps() != 5 {
		t.Fatalf("stats = %+v", st)
	}
	if st.LossRecoveryRate() != 0 {
		t.Fatalf("recovery = %v", st.LossRecoveryRate())
	}
	// Sliding forward again marks the newly skipped slots unrecoverable.
	if _, err := w.Push(Packet{Sequence: 200}); err != nil {
		t.Fatalf("far-ahead packet should not fail the run: %v", err)
	}
	if w.Stats().Unrecoverable <= 5 {
		t.Fatalf("unrecoverable = %d", w.Stats().Unrecoverable)
	}
}

func TestLossWithinWindowIsRecovered(t *testing.T) {
	w, _ := New(Config{Size: 8})
	var got []uint16
	push := func(s uint16) {
		t.Helper()
		out, err := w.Push(Packet{Sequence: s})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, seqOf(out)...)
	}
	// 3 is missing; 4..7 arrive and buffer, then 3 arrives late.
	for _, s := range []uint16{0, 1, 2, 4, 5, 6, 7} {
		push(s)
	}
	push(3)
	got = append(got, seqOf(w.Finalise())...)
	want := []uint16{0, 1, 2, 3, 4, 5, 6, 7}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v want %v", got, want)
		}
	}
	st := w.Stats()
	if st.Unrecoverable != 0 || st.Reordered < 4 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestFlushReportsTailHole(t *testing.T) {
	w, _ := New(Config{Size: 4})
	for _, s := range []uint16{0, 2, 3} {
		if _, err := w.Push(Packet{Sequence: s}); err != nil {
			t.Fatal(err)
		}
	}
	// The hole at 1 is declared lost; 2 and 3 are still delivered so the
	// caller sees exactly which interval is missing.
	got := seqOf(w.Finalise())
	if len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("flush delivered %v", got)
	}
	// Slot 1 never arrived; the stream ended while it was still inside the
	// window, so it is tail loss, not a beyond-window loss.
	st := w.Stats()
	if st.TailLoss != 1 || st.Unrecoverable != 0 || st.Recovered != 0 || st.Committed != 3 {
		t.Fatalf("stats = %+v", st)
	}
	if st.LossRecoveryRate() != 0 {
		t.Fatalf("recovery = %v", st.LossRecoveryRate())
	}
}

func TestSequenceWraparound(t *testing.T) {
	w, _ := New(Config{Size: 4})
	var got []uint16
	for _, s := range []uint16{65534, 65535, 0, 1} {
		out, err := w.Push(Packet{Sequence: s})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, seqOf(out)...)
	}
	if len(got) != 4 || got[2] != 0 || got[3] != 1 {
		t.Fatalf("wrap order = %v", got)
	}
}

func TestBufferLimit(t *testing.T) {
	w, _ := New(Config{Size: 16, MaxBuffered: 2})
	for _, s := range []uint16{0, 2, 3} {
		if _, err := w.Push(Packet{Sequence: s}); err != nil {
			t.Fatalf("push %d: %v", s, err)
		}
	}
	if _, err := w.Push(Packet{Sequence: 4}); err == nil {
		t.Fatal("expected buffer-full error")
	}
}

func TestNonZeroStartSequence(t *testing.T) {
	w, _ := New(Config{Size: 8})
	var got []uint16
	for _, s := range []uint16{50_000, 50_002, 50_001} {
		out, err := w.Push(Packet{Sequence: s})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, seqOf(out)...)
	}
	if len(got) != 3 || got[0] != 50_000 || got[1] != 50_001 || got[2] != 50_002 {
		t.Fatalf("output = %v", got)
	}
}

func TestStreamCancellation(t *testing.T) {
	w, _ := New(Config{Size: 4})
	ctx, cancel := context.WithCancel(context.Background())
	in := make(chan Packet)
	out := make(chan Delivered, 1)
	cancel()
	err := w.Stream(ctx, in, out)
	var me *model.Error
	if !errors.As(err, &me) || me.Kind != model.KindCancelled {
		t.Fatalf("want cancelled, got %v", err)
	}
}

func TestStreamPropagates(t *testing.T) {
	w, _ := New(Config{Size: 4})
	ctx := context.Background()
	in := make(chan Packet, 6)
	out := make(chan Delivered, 16)
	for _, s := range []uint16{0, 2, 1, 3} {
		in <- Packet{Sequence: s, Timestamp: uint32(s)}
	}
	close(in)
	if err := w.Stream(ctx, in, out); err != nil {
		t.Fatal(err)
	}
	close(out)
	var got []uint16
	for d := range out {
		got = append(got, d.Sequence)
	}
	for i, want := range []uint16{0, 1, 2, 3} {
		if got[i] != want {
			t.Fatalf("order = %v", got)
		}
	}
}

func TestWindowSizeValidation(t *testing.T) {
	if _, err := New(Config{Size: 40000}); err == nil {
		t.Fatal("expected oversized window rejection")
	}
}
