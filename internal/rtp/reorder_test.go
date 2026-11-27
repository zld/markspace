package rtp

import (
	"testing"

	"github.com/pion/rtp"
)

func marshal(seq uint16) []byte {
	p := rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, SSRC: 7}, Payload: []byte{1}}
	b, err := p.Marshal()
	if err != nil {
		panic(err)
	}
	return b
}

func pushAll(r *Reorderer, seqs ...uint16) {
	for i, s := range seqs {
		if d := r.Push(marshal(s), int64(i*20)); d != nil {
			panic(d)
		}
	}
}

func TestInOrderNoLoss(t *testing.T) {
	r := NewReorderer(4)
	pushAll(r, 0, 1, 2, 3, 4, 5)
	got := r.Drain(true)
	if len(got) != 6 {
		t.Fatalf("delivered %d, want 6", len(got))
	}
	st := r.Stats()
	if st.Unique != 6 || st.Lost != 0 || st.Dups != 0 || st.Received != 6 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestReorderWithinWindow(t *testing.T) {
	r := NewReorderer(4)
	pushAll(r, 0, 2, 1, 3, 4)
	got := r.Drain(true)
	var seqs []uint64
	for _, p := range got {
		seqs = append(seqs, p.ExtSeq)
	}
	for i, want := range []uint64{0, 1, 2, 3, 4} {
		if seqs[i] != want {
			t.Fatalf("order = %v", seqs)
		}
	}
	if r.Stats().Lost != 0 {
		t.Fatalf("lost = %d, want 0", r.Stats().Lost)
	}
}

func TestDuplicatesCountedOnce(t *testing.T) {
	r := NewReorderer(4)
	pushAll(r, 0, 1, 1, 1, 2)
	r.Drain(true)
	st := r.Stats()
	if st.Received != 5 || st.Dups != 2 || st.Unique != 3 {
		t.Fatalf("stats = %+v, want received=5 dups=2 unique=3", st)
	}
}

func TestLossAfterWindowCloses(t *testing.T) {
	r := NewReorderer(3)
	pushAll(r, 0, 1, 4, 5, 6) // 2,3 missing
	r.Drain(true)
	st := r.Stats()
	if st.Lost != 2 || st.Unique != 5 {
		t.Fatalf("stats = %+v, want lost=2 unique=5", st)
	}
}

func TestWraparound(t *testing.T) {
	r := NewReorderer(4)
	pushAll(r, 65534, 65535, 0, 1, 2)
	got := r.Drain(true)
	if len(got) != 5 {
		t.Fatalf("delivered %d", len(got))
	}
	for i, want := range []uint64{65534, 65535, 65536, 65537, 65538} {
		if got[i].ExtSeq != want {
			t.Fatalf("ext[%d] = %d, want %d", i, got[i].ExtSeq, want)
		}
	}
	if r.Stats().Lost != 0 {
		t.Fatalf("lost = %d, want 0", r.Stats().Lost)
	}
}

func TestBadPacketRecoverable(t *testing.T) {
	r := NewReorderer(4)
	d := r.Push([]byte{0xff, 0x00}, 42)
	if d == nil || !d.Recoverable || d.Offset != 42 {
		t.Fatalf("diag = %+v", d)
	}
}
