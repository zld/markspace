package jitter

import "testing"

func frame(seq uint16) Frame { return Frame{Seq: seq, PCM: []byte{byte(seq)}} }

func TestWindowInOrder(t *testing.T) {
	w := NewWindow(8)
	for i := uint16(0); i < 5; i++ {
		if r := w.Insert(frame(i)); r != Inserted {
			t.Fatalf("insert %d: %v", i, r)
		}
	}
	if w.Len() != 5 {
		t.Fatalf("len %d", w.Len())
	}
	for i := uint16(0); i < 5; i++ {
		f, ok := w.PopNext()
		if !ok || f.Seq != i {
			t.Fatalf("pop %d: ok=%v seq=%d", i, ok, f.Seq)
		}
	}
	if _, ok := w.PopNext(); ok {
		t.Fatal("expected miss on empty window")
	}
}

func TestWindowReorderDuplicateLate(t *testing.T) {
	w := NewWindow(8)
	w.Insert(frame(10))
	w.Insert(frame(12)) // reorder gap
	w.Insert(frame(11))
	if r := w.Insert(frame(11)); r != Duplicate {
		t.Fatalf("dup: %v", r)
	}
	if w.Len() != 3 {
		t.Fatalf("len %d", w.Len())
	}
	for want := uint16(10); want < 13; want++ {
		if f, ok := w.PopNext(); !ok || f.Seq != want {
			t.Fatalf("pop want %d got %v ok=%v", want, f.Seq, ok)
		}
	}
	// seq 11 is now behind the cursor and not queued: late.
	if r := w.Insert(frame(11)); r != TooLate {
		t.Fatalf("late: %v", r)
	}
}

func TestWindowWraparound(t *testing.T) {
	w := NewWindow(8)
	for _, s := range []uint16{65534, 65535, 0, 1} {
		if r := w.Insert(frame(s)); r != Inserted {
			t.Fatalf("insert %d: %v", s, r)
		}
	}
	for _, want := range []uint16{65534, 65535, 0, 1} {
		f, ok := w.PopNext()
		if !ok || f.Seq != want {
			t.Fatalf("pop want %d got %d ok=%v", want, f.Seq, ok)
		}
	}
	// 65535 is now behind the cursor: late, not new.
	if r := w.Insert(frame(65535)); r != TooLate {
		t.Fatalf("late after wrap: %v", r)
	}
}

func TestWindowOverflow(t *testing.T) {
	w := NewWindow(3)
	w.Insert(frame(0))
	w.Insert(frame(1))
	w.Insert(frame(2))
	if r := w.Insert(frame(3)); r != Overflow {
		t.Fatalf("overflow: %v", r)
	}
	// A far-future seq beyond capacity is also overflow.
	if r := w.Insert(frame(100)); r != Overflow {
		t.Fatalf("far future: %v", r)
	}
}

func TestWindowEvictBeyond(t *testing.T) {
	w := NewWindow(8)
	for i := uint16(0); i < 6; i++ {
		w.Insert(frame(i))
	}
	if n := w.EvictBeyond(2); n != 4 {
		t.Fatalf("evicted %d", n)
	}
	if w.Len() != 2 {
		t.Fatalf("len %d", w.Len())
	}
}
