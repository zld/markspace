package packet

import (
	"testing"

	"github.com/pion/rtp"
)

func marshal(t *testing.T, seq uint16, ts uint32, payload []byte) []byte {
	t.Helper()
	p := rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: 0xbeef},
		Payload: payload,
	}
	b, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseRoundTrip(t *testing.T) {
	raw := marshal(t, 1000, 90000, []byte{1, 2, 3})
	rec, err := Parse(raw, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Seq != 1000 || rec.Timestamp != 90000 || rec.SSRC != 0xbeef || rec.Size != len(raw) {
		t.Fatalf("record: %+v", rec)
	}
	if _, err := Parse([]byte{0, 1}, 0, 0); err == nil {
		t.Fatal("short packet must fail")
	}
}

// feed pushes the given sequence numbers through a Reorderer.
func feed(seqs ...uint16) Stats {
	r := NewReorderer(8)
	for i, s := range seqs {
		r.Push(Record{Seq: s, Arrival: i, ArrivalMs: int64(i) * 20, Size: 100})
	}
	return r.Flush()
}

func TestCleanStream(t *testing.T) {
	st := feed(1000, 1001, 1002, 1003)
	if st.Duplicates != 0 || st.Reordered != 0 || st.Lost() != 0 || st.Delivered != 4 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestDuplicateNotCountedAsLoss(t *testing.T) {
	st := feed(1000, 1001, 1001, 1002)
	if st.Duplicates != 1 {
		t.Fatalf("duplicates: %d", st.Duplicates)
	}
	if st.Lost() != 0 || st.Delivered != 3 {
		t.Fatalf("duplicate must not count as loss: %+v", st)
	}
}

func TestInWindowReorder(t *testing.T) {
	// 1012 and 1013 arrive before 1011: two out-of-order deliveries.
	st := feed(1010, 1012, 1013, 1011, 1014)
	if st.Reordered != 2 {
		t.Fatalf("reordered: %d", st.Reordered)
	}
	if st.Lost() != 0 || st.Delivered != 5 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestBurstLossDeclaredBeyondWindow(t *testing.T) {
	// 1016..1018 never arrive; the gap is declared once a packet lands
	// more than a window ahead.
	seqs := []uint16{1014, 1015}
	for s := uint16(1019); s <= 1026; s++ {
		seqs = append(seqs, s)
	}
	st := feed(seqs...)
	if st.Lost() != 3 || st.MaxBurst() != 3 {
		t.Fatalf("burst: %+v gaps=%+v", st, st.Gaps)
	}
	if len(st.Gaps) != 1 || st.Gaps[0].From != 1016 || st.Gaps[0].Len != 3 {
		t.Fatalf("gap: %+v", st.Gaps)
	}
	if st.Reordered != 0 {
		t.Fatalf("packets held by a gap are not reorders: %d", st.Reordered)
	}
}

func TestFlushDeclaresTailGap(t *testing.T) {
	st := feed(2000, 2001, 2004)
	if st.Lost() != 2 || st.Gaps[0].From != 2002 {
		t.Fatalf("tail gap: %+v", st.Gaps)
	}
	if st.Delivered != 3 {
		t.Fatalf("delivered: %d", st.Delivered)
	}
}

func TestBandwidthSamples(t *testing.T) {
	r := NewReorderer(8)
	for i := 0; i < 10; i++ {
		r.Push(Record{Seq: uint16(3000 + i), Arrival: i, ArrivalMs: int64(i) * 250, Size: 12500})
	}
	st := r.Flush()
	if len(st.Bandwidth) != 5 { // 2500ms / 500ms buckets
		t.Fatalf("buckets: %+v", st.Bandwidth)
	}
	// 2 packets x 12500B per 500ms bucket = 400 kbps.
	if st.Bandwidth[0].Kbps != 400 {
		t.Fatalf("kbps: %v", st.Bandwidth[0].Kbps)
	}
}
