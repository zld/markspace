package rtp

import "testing"

func TestWindowInOrder(t *testing.T) {
	w := NewWindow(4)
	var got []Delivered
	for i := uint16(0); i < 10; i++ {
		_, d := w.Push(i, []byte{byte(i)})
		got = append(got, d...)
	}
	d, gaps := w.Flush()
	got = append(got, d...)
	if len(got) != 10 || len(gaps) != 0 {
		t.Fatalf("delivered=%d gaps=%d", len(got), len(gaps))
	}
	st := w.Stats()
	if st.Reordered != 0 || st.Duplicates != 0 {
		t.Fatalf("unexpected stats %+v", st)
	}
}

func TestWindowReorder(t *testing.T) {
	w := NewWindow(8)
	// 0,1 then 3,2: packet 3 buffered, 2 fills the hole and both drain.
	w.Push(0, []byte{0})
	w.Push(1, []byte{1})
	if _, out := w.Push(3, []byte{3}); len(out) != 0 {
		t.Fatal("seq 3 should be buffered")
	}
	_, out := w.Push(2, []byte{2})
	if len(out) != 2 || out[0].Seq != 2 || out[1].Seq != 3 {
		t.Fatalf("expected 2,3 delivery, got %+v", out)
	}
	if w.Stats().Reordered != 1 {
		t.Fatalf("reordered=%d want 1 (packet 2 arrived after 3)", w.Stats().Reordered)
	}
}

func TestWindowDuplicate(t *testing.T) {
	w := NewWindow(4)
	_, first := w.Push(5, []byte("a"))
	if len(first) != 1 || string(first[0].Payload) != "a" {
		t.Fatalf("first packet not delivered: %+v", first)
	}
	o, _ := w.Push(5, []byte("b"))
	if o != OutDuplicate || w.Stats().Duplicates != 1 {
		t.Fatalf("duplicate not detected: %+v", w.Stats())
	}
	if _, gaps := w.Flush(); len(gaps) != 0 {
		t.Fatalf("duplicate must not create a gap: %+v", gaps)
	}
}

func TestWindowBurstLossAndNoFabrication(t *testing.T) {
	w := NewWindow(4)
	var delivered []Delivered
	for _, s := range []uint16{0, 1, 2, 3, 4, 8, 9, 10} {
		_, d := w.Push(s, []byte{byte(s)})
		delivered = append(delivered, d...)
	}
	rest, gaps := w.Flush()
	delivered = append(delivered, rest...)
	if len(gaps) != 1 || gaps[0].StartSeq != 5 || gaps[0].EndSeq != 7 {
		t.Fatalf("want single gap 5..7, got %+v", gaps)
	}
	if len(delivered) != 8 {
		t.Fatalf("want 8 real payloads (8 received, 3 missing), got %d", len(delivered))
	}
	for _, p := range delivered {
		if p.Seq >= 5 && p.Seq <= 7 {
			t.Fatal("missing packet must never be fabricated")
		}
	}
}

func TestWindowLateInsideWindowReinstated(t *testing.T) {
	w := NewWindow(16)
	for _, s := range []uint16{0, 1, 2, 4, 5} {
		w.Push(s, []byte{byte(s)})
	}
	// seq 3 is still within the window: it must fill the hole, no gap.
	w.Push(3, []byte{3})
	_, gaps := w.Flush()
	if len(gaps) != 0 {
		t.Fatalf("late in-window packet should be placed, gaps=%+v", gaps)
	}
}

func TestWindowLateOutsideWindowIsGap(t *testing.T) {
	w := NewWindow(4)
	for s := uint16(0); s <= 12; s++ {
		if s == 3 {
			continue // drop seq 3
		}
		w.Push(s, []byte{byte(s)})
	}
	// Now seq 3 is far below the finalized edge.
	o, _ := w.Push(3, []byte{3})
	if o != OutLateGap {
		t.Fatalf("want OutLateGap, got %d", o)
	}
	gaps := w.Gaps()
	found := false
	for _, g := range gaps {
		if g.StartSeq == 3 && g.EndSeq == 3 {
			found = true
		}
	}
	if !found {
		t.Fatalf("gap 3 missing: %+v", gaps)
	}
}

func TestWindowWrapAround(t *testing.T) {
	w := NewWindow(8)
	start := uint16(65530)
	seqs := []uint16{start, start + 1, start + 2, start + 3, start + 4, start + 5, start + 6}
	var d []Delivered
	for _, s := range seqs {
		_, out := w.Push(s, []byte{byte(s)})
		d = append(d, out...)
	}
	rest, gaps := w.Flush()
	d = append(d, rest...)
	if len(gaps) != 0 || len(d) != 7 {
		t.Fatalf("wrap delivery wrong: %d gaps=%+v", len(d), gaps)
	}
	if d[6].Seq != 65536 {
		t.Fatalf("extended seq wrong: %d", d[6].Seq)
	}
	// Duplicate detection must use extended numbers: start+6 == 65536
	// wraps to raw seq 0.
	if o, _ := w.Push(0, []byte("x")); o != OutDuplicate {
		t.Fatalf("wrap duplicate not detected: %d", o)
	}
}

func TestUnwrap(t *testing.T) {
	cases := []struct {
		seq  uint16
		hi   uint64
		want uint64
	}{
		{5, 10, 5},
		{65530, 4, ^uint64(0) - 5}, // nearest candidate is -6, i.e. one wrap below
		{10, 65534, 65546},
	}
	for _, c := range cases {
		if got := unwrap(c.seq, c.hi); got != c.want {
			t.Fatalf("unwrap(%d,%d)=%d want %d", c.seq, c.hi, got, c.want)
		}
	}
}
