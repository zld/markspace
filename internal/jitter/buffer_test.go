package jitter

import (
	"testing"

	"github.com/pion/rtp"

	"mediarun/internal/fault"
	rtpparse "mediarun/internal/rtp"
)

func pkt(seq uint16, offset int64) *rtpparse.Packet {
	return &rtpparse.Packet{
		Offset:   offset,
		Sequence: seq,
		Payload:  []byte{byte(seq), byte(seq >> 8)},
		Header:   rtp.Header{SequenceNumber: seq},
	}
}

func newBuf(t *testing.T, window uint64, max int) *Buffer {
	t.Helper()
	b, err := New(Config{Window: window, MaxPackets: max})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func seqs(pkts []*rtpparse.Packet) []uint64 {
	out := make([]uint64, len(pkts))
	for i, p := range pkts {
		out[i] = p.ExtSeq
	}
	return out
}

func TestBufferOrdersByExtendedSequence(t *testing.T) {
	b := newBuf(t, 4, 64)
	var committed []uint64
	for _, seq := range []uint16{10, 12, 11, 13, 14, 15, 16} {
		res := b.Push(pkt(seq, 0))
		committed = append(committed, seqs(res.Committed)...)
	}
	committed = append(committed, seqs(b.Flush())...)
	want := []uint64{10, 11, 12, 13, 14, 15, 16}
	if len(committed) != len(want) {
		t.Fatalf("committed = %v want %v", committed, want)
	}
	for i := range want {
		if committed[i] != want[i] {
			t.Fatalf("committed = %v want %v", committed, want)
		}
	}
	st := b.Stats()
	if st.Duplicates != 0 || st.Late != 0 || st.Received != 7 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestBufferDeduplicates(t *testing.T) {
	b := newBuf(t, 2, 64)
	b.Push(pkt(1, 0))
	b.Push(pkt(1, 0))
	res := b.Push(pkt(2, 0))
	if res.Reason != ReasonNone {
		t.Fatalf("reason = %s", res.Reason)
	}
	if b.Stats().Duplicates != 1 {
		t.Fatalf("duplicates = %d", b.Stats().Duplicates)
	}
	if b.Faults().CountKind(fault.KindDuplicate) != 1 {
		t.Fatalf("faults = %s", b.Faults().Summary())
	}
}

func TestBufferLatePacketCannotRewriteCommit(t *testing.T) {
	b := newBuf(t, 2, 64)
	var committed []uint64
	for seq := uint16(1); seq <= 5; seq++ {
		committed = append(committed, seqs(b.Push(pkt(seq, int64(seq))).Committed)...)
	}
	before := append([]uint64(nil), committed...)
	res := b.Push(pkt(2, 999))
	if res.Reason != ReasonLate {
		t.Fatalf("reason = %s want late", res.Reason)
	}
	if len(res.Committed) != 0 {
		t.Fatal("late packet released data")
	}
	if len(before) != len(committed) {
		t.Fatal("commit stream changed")
	}
	if b.Faults().CountKind(fault.KindLate) != 1 {
		t.Fatalf("faults = %s", b.Faults().Summary())
	}
}

func TestBufferWindowOverflowRecordsReason(t *testing.T) {
	b := newBuf(t, 100, 3)
	if res := b.Push(pkt(1, 10)); res.Reason != ReasonNone {
		t.Fatalf("first = %s", res.Reason)
	}
	b.Push(pkt(2, 20))
	b.Push(pkt(3, 30))
	res := b.Push(pkt(4, 40))
	if res.Reason != ReasonOverflow {
		t.Fatalf("reason = %s want overflow", res.Reason)
	}
	fs := b.Faults()
	if fs.CountKind(fault.KindWindow) != 1 {
		t.Fatalf("faults = %s", fs.Summary())
	}
	if fs[0].Offset != 40 || fs[0].Kind != fault.KindWindow {
		t.Fatalf("fault = %v", fs[0])
	}
	if b.Stats().Overflow != 1 {
		t.Fatalf("stats = %+v", b.Stats())
	}
}

func TestBufferWrapAroundCommit(t *testing.T) {
	b := newBuf(t, 3, 64)
	var out []uint64
	for _, seq := range []uint16{65532, 65533, 65534, 65535, 0, 1, 2} {
		out = append(out, seqs(b.Push(pkt(seq, 0)).Committed)...)
	}
	out = append(out, seqs(b.Flush())...)
	want := []uint64{65532, 65533, 65534, 65535, 65536, 65537, 65538}
	if len(out) != len(want) {
		t.Fatalf("out = %v", out)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("out = %v want %v", out, want)
		}
	}
}

func TestBufferMissingRangesAndRun(t *testing.T) {
	b := newBuf(t, 100, 64)
	for _, seq := range []uint16{1, 2, 5, 6, 8} {
		b.Push(pkt(seq, 0))
	}
	gaps := b.Missing()
	want := [][2]uint64{{3, 5}, {7, 8}}
	if len(gaps) != len(want) {
		t.Fatalf("gaps = %v want %v", gaps, want)
	}
	for i := range want {
		if gaps[i] != want[i] {
			t.Fatalf("gaps = %v want %v", gaps, want)
		}
	}
	if run := b.ContiguousRun(); run != 2 {
		t.Fatalf("contiguous run = %d want 2", run)
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	if _, err := New(Config{Window: 0, MaxPackets: 1}); err == nil {
		t.Fatal("zero window accepted")
	}
	if _, err := New(Config{Window: 1, MaxPackets: 0}); err == nil {
		t.Fatal("zero max packets accepted")
	}
}

func TestBufferFaultListBounded(t *testing.T) {
	b := newBuf(t, 100, 4)
	b.Push(pkt(1, 0))
	for i := 0; i < 5000; i++ {
		b.Push(pkt(1, 0))
	}
	if len(b.Faults()) > 1024 {
		t.Fatalf("fault list grew to %d", len(b.Faults()))
	}
}
