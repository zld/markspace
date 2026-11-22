package rtprecover

import (
	"testing"

	"github.com/pion/rtp"
)

func pkt(seq uint16) *rtp.Packet {
	return &rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * 100},
		Payload: []byte{0, byte(seq >> 8), byte(seq)},
	}
}

func collect(w *Window) []uint16 {
	var out []uint16
	_ = w.Flush(func(p *rtp.Packet) error {
		out = append(out, p.SequenceNumber)
		return nil
	})
	return out
}

func TestOrderedAndReordered(t *testing.T) {
	w := NewWindow(8)
	for _, seq := range []uint16{0, 1, 2} {
		if dropped, _ := w.Push(pkt(seq)); dropped {
			t.Fatalf("seq %d unexpectedly dropped", seq)
		}
	}
	if got := collect(w); len(got) != 3 || got[2] != 2 {
		t.Fatalf("ordered flush = %v", got)
	}
	// Reordered arrival: 4 before 3.
	if dropped, _ := w.Push(pkt(4)); dropped {
		t.Fatal("seq 4 must be buffered")
	}
	if got := collect(w); len(got) != 0 {
		t.Fatalf("hole must block flush, got %v", got)
	}
	if dropped, _ := w.Push(pkt(3)); dropped {
		t.Fatal("seq 3 must be buffered")
	}
	if got := collect(w); len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("reorder flush = %v", got)
	}
}

func TestDuplicates(t *testing.T) {
	w := NewWindow(4)
	if dropped, _ := w.Push(pkt(5)); dropped {
		t.Fatal("seq 5 unexpectedly dropped")
	}
	if dropped, _ := w.Push(pkt(6)); dropped {
		t.Fatal("seq 6 unexpectedly dropped")
	}
	dropped, kind := w.Push(pkt(5))
	if !dropped || kind != DropDuplicate {
		t.Fatalf("buffered duplicate: dropped=%v kind=%s", dropped, kind)
	}
	_ = collect(w)
	// Replayed after playout must still be recognized as a duplicate.
	dropped, kind = w.Push(pkt(5))
	if !dropped || kind != DropDuplicate {
		t.Fatalf("played duplicate: dropped=%v kind=%s", dropped, kind)
	}
	if w.Stats().Duplicates != 2 {
		t.Fatalf("duplicates = %d, want 2", w.Stats().Duplicates)
	}
	if w.Stats().Emitted != 2 {
		t.Fatalf("emitted = %d, want 2 (duplicate must not re-emit)", w.Stats().Emitted)
	}
}

func TestLateOutsideWindow(t *testing.T) {
	w := NewWindow(4)
	_, _ = w.Push(pkt(0))
	// seq 10 is 10 ahead of head 0; window capacity is 4 slots.
	dropped, kind := w.Push(pkt(10))
	if !dropped || kind != DropLate {
		t.Fatalf("far jump: dropped=%v kind=%s", dropped, kind)
	}
	// Fill 1..3 and flush so the head advances; the played seqs stay in
	// history long enough for retransmissions to be classified correctly.
	for _, s := range []uint16{1, 2, 3} {
		if dropped, _ := w.Push(pkt(s)); dropped {
			t.Fatalf("seq %d unexpectedly dropped", s)
		}
	}
	_ = collect(w)
	if head, _ := w.Head(); head != 4 {
		t.Fatalf("head = %d, want 4", head)
	}
	dropped, kind = w.Push(pkt(0))
	if !dropped || kind != DropDuplicate {
		t.Fatalf("replayed packet must be a duplicate: dropped=%v kind=%s", dropped, kind)
	}
	st := w.Stats()
	if st.LateDropped != 1 || st.Duplicates != 1 || st.Emitted != 4 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestAncientLatePacket(t *testing.T) {
	// A packet older than the history bitmap cannot be proven duplicate and
	// must be counted as a window-out late arrival.
	w := NewWindow(2)
	for _, s := range []uint16{0, 1, 2, 3, 4, 5} {
		if dropped, _ := w.Push(pkt(s)); dropped {
			t.Fatalf("seq %d unexpectedly dropped", s)
		}
		_ = collect(w)
	}
	// Head is now 6; seq 0 has rotated beyond the 4-slot history window.
	dropped, kind := w.Push(pkt(0))
	if !dropped || kind != DropLate {
		t.Fatalf("ancient packet: dropped=%v kind=%s", dropped, kind)
	}
	if w.Stats().LateDropped != 1 {
		t.Fatalf("late = %d, want 1", w.Stats().LateDropped)
	}
}

func TestSequenceWrap(t *testing.T) {
	w := NewWindow(8)
	for _, s := range []uint16{65533, 65534, 65535} {
		if dropped, _ := w.Push(pkt(s)); dropped {
			t.Fatalf("seq %d dropped", s)
		}
	}
	if dropped, _ := w.Push(pkt(65534)); !dropped {
		t.Fatal("wrapped duplicate not detected")
	}
	if dropped, _ := w.Push(pkt(0)); dropped {
		t.Fatal("seq 0 after 65535 must be accepted across wrap")
	}
	if dropped, _ := w.Push(pkt(1)); dropped {
		t.Fatal("seq 1 must be accepted across wrap")
	}
	got := collect(w)
	want := []uint16{65533, 65534, 65535, 0, 1}
	if len(got) != len(want) {
		t.Fatalf("wrap flush = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("wrap flush = %v, want %v", got, want)
		}
	}
}

func TestBoundedMemoryAndPeak(t *testing.T) {
	// With a four-slot window, base seq 0 occupies slot 0; a hole at seq 1
	// leaves slots for 2 and 3, so seq 4 is the first packet the window
	// cannot hold without growing.
	w := NewWindow(4)
	for _, s := range []uint16{0, 2, 3} {
		if dropped, _ := w.Push(pkt(s)); dropped {
			t.Fatalf("seq %d must fit inside the four-slot window", s)
		}
	}
	if w.Held() != 3 {
		t.Fatalf("held = %d, want 3", w.Held())
	}
	if w.Stats().PeakHeld != 3 {
		t.Fatalf("peak = %d, want 3", w.Stats().PeakHeld)
	}
	// A fourth in-flight packet beyond the ring cannot grow the buffer.
	dropped, kind := w.Push(pkt(4))
	if !dropped || kind != DropLate {
		t.Fatalf("overflow packet not rejected: %v %s", dropped, kind)
	}
	if w.Held() != 3 {
		t.Fatalf("window grew beyond capacity: %d", w.Held())
	}
}

func TestSkipHead(t *testing.T) {
	w := NewWindow(4)
	for _, s := range []uint16{0, 2} {
		if dropped, _ := w.Push(pkt(s)); dropped {
			t.Fatalf("seq %d unexpectedly dropped", s)
		}
	}
	if got := collect(w); len(got) != 1 || got[0] != 0 {
		t.Fatalf("initial flush = %v, want [0]", got)
	}
	// Head is now the hole at seq 1; declare it missing.
	w.SkipHead()
	got := collect(w)
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("after skip flush = %v, want [2]", got)
	}
	// A retransmission of the skipped seq must now be treated as duplicate.
	if dropped, kind := w.Push(pkt(1)); !dropped || kind != DropDuplicate {
		t.Fatalf("skipped hole re-arrival: dropped=%v kind=%s", dropped, kind)
	}
}
