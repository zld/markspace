package buffer

import (
	"testing"

	"jitterlab/internal/packet"
)

func frame(seq uint64) packet.Frame {
	return packet.Frame{Version: packet.Version, Seq: seq}
}

func mustWindow(t *testing.T, size int) *Window {
	t.Helper()
	w, err := NewWindow(size)
	if err != nil {
		t.Fatalf("NewWindow(%d): %v", size, err)
	}
	return w
}

func TestWindowRejectsInvalidSize(t *testing.T) {
	for _, size := range []int{0, -1, MaxWindowSize + 1} {
		if _, err := NewWindow(size); err == nil {
			t.Fatalf("NewWindow(%d) accepted", size)
		}
	}
}

func TestWindowReorderAndDuplicate(t *testing.T) {
	w := mustWindow(t, 8)

	if got := w.Push(frame(10)); got.Status != Accepted {
		t.Fatalf("first push = %v", got.Status)
	}
	if got := w.Push(frame(12)); got.Status != Accepted || got.Reordered {
		t.Fatalf("push 12 = %+v, want accepted without reorder", got)
	}
	if got := w.Push(frame(11)); got.Status != Accepted || !got.Reordered {
		t.Fatalf("push 11 = %+v, want accepted+reordered", got)
	}

	f, ok := w.Pop()
	if !ok || f.Seq != 10 {
		t.Fatalf("pop = %+v %v, want 10", f, ok)
	}
	// The earliest arrival must be playable even though 12 came first.
	if f, ok = w.Pop(); !ok || f.Seq != 11 {
		t.Fatalf("pop = %+v %v, want 11", f, ok)
	}
	if f, ok = w.Pop(); !ok || f.Seq != 12 {
		t.Fatalf("pop = %+v %v, want 12", f, ok)
	}
	if _, ok = w.Pop(); ok {
		t.Fatal("popped beyond buffered frames")
	}

	if got := w.Push(frame(12)); got.Status != Duplicate {
		t.Fatalf("replay of delivered 12 = %v, want duplicate", got)
	}
	w.Push(frame(20))
	if got := w.Push(frame(20)); got.Status != Duplicate {
		t.Fatalf("duplicate pending = %v, want duplicate", got)
	}
}

func TestWindowLatePacketCannotOverwrite(t *testing.T) {
	w := mustWindow(t, 8)
	for _, seq := range []uint64{1, 2, 3} {
		w.Push(frame(seq))
	}
	for i := 0; i < 3; i++ {
		if _, ok := w.Pop(); !ok {
			t.Fatalf("pop %d failed", i)
		}
	}
	if got := w.Push(frame(2)); got.Status != Duplicate {
		t.Fatalf("late replay = %v, want duplicate", got)
	}
	if got := w.Push(frame(0)); got.Status != Expired {
		t.Fatalf("late unseen = %v, want expired", got)
	}
	if w.Next() != 4 {
		t.Fatalf("next = %d, want 4", w.Next())
	}
}

func TestWindowOutOfSpan(t *testing.T) {
	w := mustWindow(t, 4)
	w.Push(frame(100))
	if got := w.Push(frame(104)); got.Status != Expired {
		t.Fatalf("seq at next+size = %v, want expired", got)
	}
	if got := w.Push(frame(103)); got.Status != Accepted {
		t.Fatalf("seq at next+size-1 = %v, want accepted", got)
	}
}

func TestWindowIgnoresLateForerunnerAfterStart(t *testing.T) {
	w := mustWindow(t, 8)
	w.Push(frame(50))
	w.Pop()
	w.SkipNext() // 51 abandoned
	if got := w.Push(frame(49)); got.Status != Expired {
		t.Fatalf("late forerunner = %v, want expired", got)
	}
}

func TestWindowSequenceWrap(t *testing.T) {
	w := mustWindow(t, 16)
	const near = (1 << 40) - 2
	for _, seq := range []uint64{near, near + 1, near + 2, near + 3} {
		res := w.Push(frame(seq))
		if res.Status != Accepted {
			t.Fatalf("push %d = %v, want accepted", packet.MaskSeq(seq), res.Status)
		}
	}
	for i := uint64(0); i < 4; i++ {
		f, ok := w.Pop()
		if !ok {
			t.Fatalf("pop %d failed after wrap", i)
		}
		if want := packet.MaskSeq(near + i); f.Seq != want {
			t.Fatalf("pop %d seq = %d, want %d", i, f.Seq, want)
		}
	}
	if got := w.Push(frame((1 << 40) + 1)); got.Status != Duplicate {
		t.Fatalf("wrapped duplicate = %v, want duplicate", got)
	}
}

func TestWindowSkipToDropsStaleFrames(t *testing.T) {
	w := mustWindow(t, 32)
	for _, seq := range []uint64{5, 6, 7, 8} {
		w.Push(frame(seq))
	}
	if n := w.SkipTo(8); n != 3 {
		t.Fatalf("SkipTo dropped %d, want 3", n)
	}
	if w.Next() != 8 || w.Len() != 1 {
		t.Fatalf("next=%d len=%d, want 8 and 1", w.Next(), w.Len())
	}
	if f, ok := w.Pop(); !ok || f.Seq != 8 {
		t.Fatalf("pop = %+v %v, want 8", f, ok)
	}
}
