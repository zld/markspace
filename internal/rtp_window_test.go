package rtpreplay

import "testing"

func pk(seq uint16) *Packet { return &Packet{Track: "opus", Seq: seq} }

func seqsOf(em []Emission) []int64 {
	out := make([]int64, 0, len(em))
	for _, e := range em {
		if e.Kind == EmitPacket {
			out = append(out, int64(e.Packet.Seq))
		} else {
			out = append(out, -int64(e.Count))
		}
	}
	return out
}

func equal(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestReorderBufferInOrder(t *testing.T) {
	b, _ := NewReorderBuffer(4)
	for i := 0; i < 10; i++ {
		b.Push(pk(uint16(i)))
	}
	out, stats := b.Finish()
	if !equal(seqsOf(out), []int64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}) {
		t.Fatalf("order=%v", seqsOf(out))
	}
	if stats.Duplicates != 0 || stats.Reordered != 0 || stats.GapPackets != 0 || stats.Delivered != 10 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestReorderBufferWrap(t *testing.T) {
	b, _ := NewReorderBuffer(0)
	base := uint16(65530)
	for i := 0; i < 12; i++ {
		b.Push(pk(base + uint16(i)))
	}
	out, stats := b.Finish()
	want := []int64{65530, 65531, 65532, 65533, 65534, 65535, 0, 1, 2, 3, 4, 5}
	if !equal(seqsOf(out), want) {
		t.Fatalf("order=%v want %v", seqsOf(out), want)
	}
	if stats.Reordered != 0 || stats.Late != 0 || stats.GapPackets != 0 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestReorderBufferDuplicateAndOutOfOrder(t *testing.T) {
	b, _ := NewReorderBuffer(8)
	order := []uint16{100, 102, 101, 101, 104, 103, 106, 105}
	for _, s := range order {
		b.Push(pk(s))
	}
	out, stats := b.Finish()
	if !equal(seqsOf(out), []int64{100, 101, 102, 103, 104, 105, 106}) {
		t.Fatalf("order=%v", seqsOf(out))
	}
	if stats.Duplicates != 1 || stats.Reordered != 3 || stats.Delivered != 7 {
		t.Fatalf("stats=%+v", stats)
	}
	if stats.Received != 8 {
		t.Fatalf("received=%d", stats.Received)
	}
}

func TestReorderBufferGapAtWindowClose(t *testing.T) {
	b, _ := NewReorderBuffer(3)
	for _, s := range []uint16{0, 1, 2, 3, 4, 8, 9, 10} {
		b.Push(pk(s))
	}
	out, stats := b.Finish()
	if !equal(seqsOf(out), []int64{0, 1, 2, 3, 4, -1, -1, -1, 8, 9, 10}) {
		t.Fatalf("order=%v", seqsOf(out))
	}
	if stats.GapPackets != 3 {
		t.Fatalf("gap=%d", stats.GapPackets)
	}
	runs := AggregateGaps(out)
	if len(runs) != 1 || runs[0].Count != 3 || runs[0].StartExtSeq != 5 || runs[0].EndExtSeq != 7 {
		t.Fatalf("runs=%+v", runs)
	}
}

func TestReorderBufferLatePacketAfterGap(t *testing.T) {
	b, _ := NewReorderBuffer(0)
	for _, s := range []uint16{0, 1, 3, 2, 4} {
		b.Push(pk(s))
	}
	out, stats := b.Finish()
	// Depth 0 closes every position immediately, so 2 arrives after its gap.
	if !equal(seqsOf(out), []int64{0, 1, -1, 3, 4}) {
		t.Fatalf("order=%v", seqsOf(out))
	}
	if stats.Late != 1 || stats.GapPackets != 1 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestReorderBufferZeroDepthReorder(t *testing.T) {
	b, _ := NewReorderBuffer(0)
	for _, s := range []uint16{10, 11, 12} {
		b.Push(pk(s))
	}
	out, _ := b.Finish()
	if !equal(seqsOf(out), []int64{10, 11, 12}) {
		t.Fatalf("order=%v", seqsOf(out))
	}
}

func TestReorderBufferDepthValidation(t *testing.T) {
	if _, err := NewReorderBuffer(-1); err == nil {
		t.Fatal("expected error")
	}
	if _, err := NewReorderBuffer(5000); err == nil {
		t.Fatal("expected error")
	}
}

func TestReorderBufferDuplicateAfterGap(t *testing.T) {
	b, _ := NewReorderBuffer(0)
	for _, s := range []uint16{0, 2, 1, 1} {
		b.Push(pk(s))
	}
	out, stats := b.Finish()
	if !equal(seqsOf(out), []int64{0, -1, 2}) {
		t.Fatalf("order=%v", seqsOf(out))
	}
	if stats.Late != 1 || stats.Duplicates != 1 || stats.Received != 4 {
		t.Fatalf("stats=%+v", stats)
	}
}
