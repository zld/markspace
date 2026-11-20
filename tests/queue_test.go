package tests

import (
	"testing"
	"time"

	"jitterlab/internal/jitter"
	"jitterlab/internal/packet"
)

func TestQueueClassification(t *testing.T) {
	cfg := jitter.DefaultQueueConfig()
	cfg.Capacity = 8
	q := jitter.NewQueue(cfg)

	push := func(seq int, ts uint32) bool {
		p, err := packet.New(1, uint32(seq), ts, 1, true, []byte{byte(seq)})
		if err != nil {
			t.Fatalf("new: %v", err)
		}
		return q.Push(p, 0)
	}
	if !push(0, 100) {
		t.Fatal("seq 0 rejected")
	}
	if push(0, 100) {
		t.Fatal("duplicate accepted")
	}
	if _, ok := q.Pop(1); !ok {
		t.Fatal("seq 0 not playable")
	}
	// The cursor now expects seq 1; move past it so seq 1 becomes late.
	q.Advance(1)
	if !push(2, 140) {
		t.Fatal("out-of-order seq 2 rejected")
	}
	if push(1, 120) {
		t.Fatal("packet behind play cursor accepted")
	}
	// A duplicate of a slot that was concealed (advanced past, never played)
	// is late, not duplicate: concealment is not delivery.
	q.Advance(1)
	if push(1, 120) {
		t.Fatal("arrival for a concealed slot accepted")
	}
	// Timestamp regression far ahead of the cursor: 10 s behind the newest
	// accepted capture time.
	earlyTS := uint32(100)
	if push(3, earlyTS-100_000) {
		t.Fatal("regressed timestamp accepted")
	}
	// Capacity 8 measured from the play cursor: seq 11 is outside the window.
	if push(11, 300) {
		t.Fatal("out-of-window packet accepted")
	}
	st := q.Stats()
	if st.Duplicate != 1 || st.Late != 2 || st.Regression != 1 || st.Overflow != 1 {
		t.Fatalf("unexpected stats: %+v", st)
	}
	if st.Accepted != 2 {
		t.Fatalf("accepted = %d, want 2", st.Accepted)
	}

	// Sequence wraparound beyond 2^32 must keep ordering intact.
	wrap := jitter.NewQueue(cfg)
	mk := func(seq uint32) *packet.Packet {
		p, _ := packet.New(1, seq, seq*20, 1, true, nil)
		return p
	}
	if !wrap.Push(mk(0xffffffff), 0) {
		t.Fatal("wrap base rejected")
	}
	if !wrap.Push(mk(0), time.Millisecond) || !wrap.Push(mk(1), 2*time.Millisecond) {
		t.Fatal("wrapped sequence rejected")
	}
	if s, ok := wrap.Pop(1); !ok || s.Seq != 0xffffffff {
		t.Fatalf("wrap pop = %+v ok=%v", s, ok)
	}
	if s, ok := wrap.Pop(1); !ok || s.Seq != 0 {
		t.Fatalf("wrap pop 2 = %+v ok=%v", s, ok)
	}
}

func TestAmbiguousSequenceDropped(t *testing.T) {
	cfg := jitter.DefaultQueueConfig()
	cfg.Capacity = 16
	q := jitter.NewQueue(cfg)
	head, _ := packet.New(1, 0, 1000, 1, true, nil)
	if !q.Push(head, 0) {
		t.Fatal("base rejected")
	}
	half := uint32(1 << 31)
	amb, _ := packet.New(1, half, 2000, 1, true, nil)
	if q.Push(amb, time.Millisecond) {
		t.Fatal("ambiguous packet accepted")
	}
	st := q.Stats()
	if st.Ambiguous != 1 {
		t.Fatalf("ambiguous = %d, want 1", st.Ambiguous)
	}
	if st.Late != 0 || st.Overflow != 0 || st.Duplicate != 0 {
		t.Fatalf("ambiguous packet polluted other counters: %+v", st)
	}
}
