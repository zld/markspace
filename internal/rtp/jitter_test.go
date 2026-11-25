package rtp

import "testing"

func TestInOrderAndReorder(t *testing.T) {
	b := NewJitterBuffer("video-hi", 16, 8)
	// Out-of-order arrival: 0, 2, 1, 3
	for _, seq := range []uint16{0, 2, 1, 3} {
		if err := b.Push(seq, []byte{byte(seq)}); err != nil {
			t.Fatalf("seq %d rejected: %v", seq, err)
		}
	}
	stats := b.Stats()
	if stats.Received != 4 || stats.OutOfOrder != 1 || stats.Duplicate != 0 || stats.Late != 0 {
		t.Fatalf("counters wrong: %+v", stats)
	}
	out := b.Flush()
	if len(out) != 4 {
		t.Fatalf("flushed %d packets", len(out))
	}
	for i, p := range out {
		if p.Sequence != uint16(i) || p.Payload[0] != byte(i) {
			t.Fatalf("position %d not ordered: %+v", i, p)
		}
	}
	if b.Len() != 0 {
		t.Fatal("flush must clear the buffer")
	}
}

func TestDuplicateAndLate(t *testing.T) {
	b := NewJitterBuffer("audio-core", 32, 4)
	for _, seq := range []uint16{10, 11, 12, 13, 14, 15} {
		_ = b.Push(seq, nil)
	}
	// Re-push an already seen sequence -> duplicate.
	if err := b.Push(12, nil); err == nil {
		t.Fatal("duplicate must be reported")
	}
	// Sequence 5 is more than window=4 behind highest 15 -> late.
	if err := b.Push(5, nil); err == nil {
		t.Fatal("late packet must be reported")
	}
	stats := b.Stats()
	if stats.Duplicate != 1 || stats.Late != 1 || stats.Received != 6 {
		t.Fatalf("counters wrong: %+v", stats)
	}
	if b.Len() != 6 {
		t.Fatal("late and duplicate packets must not be buffered")
	}
}

func TestSequenceWrapAround(t *testing.T) {
	b := NewJitterBuffer("wrap", 32, 16)
	for _, seq := range []uint16{65533, 65534, 65535, 0, 1, 2} {
		if err := b.Push(seq, nil); err != nil {
			t.Fatalf("seq %d rejected across wrap: %v", seq, err)
		}
	}
	out := b.Flush()
	want := []uint16{65533, 65534, 65535, 0, 1, 2}
	for i, p := range out {
		if p.Sequence != want[i] {
			t.Fatalf("wrap ordering wrong at %d: got %d want %d", i, p.Sequence, want[i])
		}
	}
}

func TestEvictionIsDeterministic(t *testing.T) {
	first := NewJitterBuffer("cap3", 3, 32)
	second := NewJitterBuffer("cap3", 3, 32)
	seqs := []uint16{7, 9, 8, 10, 11} // capacity 3 evicts twice, oldest first
	for _, seq := range seqs {
		_ = first.Push(seq, []byte{byte(seq)})
		_ = second.Push(seq, []byte{byte(seq)})
	}
	if first.Stats().Evicted != 2 {
		t.Fatalf("evictions = %d, want 2", first.Stats().Evicted)
	}
	ev := first.Evictions()
	// Insertion order starts 7,9,8: the two oldest retained are 7 then 9.
	if ev[0].Sequence != 7 || ev[1].Sequence != 9 {
		t.Fatalf("eviction order not oldest-first: %+v", ev)
	}
	if ev[0].Reason == "" {
		t.Fatal("eviction must record a reason kind")
	}
	if len(second.Evictions()) != len(ev) || second.Evictions()[0] != ev[0] {
		t.Fatal("identical input must yield identical eviction decisions")
	}
	out := first.Flush()
	got := []uint16{out[0].Sequence, out[1].Sequence, out[2].Sequence}
	if len(got) != 3 || got[0] != 8 || got[1] != 10 || got[2] != 11 {
		t.Fatalf("survivors wrong: %v", got)
	}
}

func TestBuffersAreIndependentPerRepresentation(t *testing.T) {
	video := NewJitterBuffer("video", 8, 8)
	audio := NewJitterBuffer("audio", 8, 8)
	for _, seq := range []uint16{0, 1, 2} {
		_ = video.Push(seq, nil)
	}
	for _, seq := range []uint16{100, 100, 101} {
		_ = audio.Push(seq, nil)
	}
	if video.Stats().Duplicate != 0 || audio.Stats().Duplicate != 1 {
		t.Fatalf("representation state leaked: video=%+v audio=%+v", video.Stats(), audio.Stats())
	}
	if video.Len() != 3 || audio.Len() != 2 {
		t.Fatalf("buffer sizes wrong: video=%d audio=%d", video.Len(), audio.Len())
	}
	if hi, ok := audio.Highest(); !ok || hi != 101 {
		t.Fatalf("audio highest wrong: %d %v", hi, ok)
	}
}
