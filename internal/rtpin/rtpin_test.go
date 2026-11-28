package rtpin

import (
	"bytes"
	"io"
	"testing"

	"github.com/pion/rtp"

	"mediakernel/internal/fault"
)

func mkRecord(t *testing.T, arrival, off int64, payload []byte, seq uint16) []byte {
	t.Helper()
	pkt := &rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(off)},
		Payload: payload,
	}
	var buf bytes.Buffer
	if err := WriteRecord(&buf, arrival, uint64(off), pkt); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRoundTripRecord(t *testing.T) {
	raw := mkRecord(t, 1000, 4096, []byte("hello"), 7)
	rec, err := ReadRecord(bytes.NewReader(raw), 0)
	if err != nil {
		t.Fatal(err)
	}
	if rec.ArrivalNs != 1000 || rec.FileOffset != 4096 || rec.Sequence != 7 {
		t.Fatalf("bad record: %+v", rec)
	}
	if string(rec.Payload) != "hello" {
		t.Fatalf("payload %q", rec.Payload)
	}
}

func TestTruncatedRecord(t *testing.T) {
	raw := mkRecord(t, 0, 0, []byte("0123456789"), 1)
	for _, cut := range []int{3, RecordHeaderLen - 1, RecordHeaderLen + 2, len(raw) - 1} {
		_, err := ReadRecord(bytes.NewReader(raw[:cut]), 64)
		if !fault.Is(err, fault.ErrTruncatedPacket) {
			t.Fatalf("cut=%d: %v", cut, err)
		}
		if off, ok := fault.OffsetOf(err); !ok || off != 64 {
			t.Fatalf("cut=%d: offset %d,%v", cut, off, ok)
		}
	}
	if _, err := ReadRecord(bytes.NewReader(nil), 0); err != io.EOF {
		t.Fatalf("empty reader: %v", err)
	}
}

func TestReassemblerDedupReorder(t *testing.T) {
	r := NewReassembler()
	mk := func(off int64, s string) Record {
		return Record{TraceOffset: 0, ArrivalNs: off + 1, FileOffset: uint64(off), Payload: []byte(s)}
	}
	// Out of order: second chunk first.
	segs, _ := r.Add(mk(3, "DEF"))
	if len(segs) != 0 {
		t.Fatal("nothing committable yet")
	}
	// Duplicate of the buffered chunk.
	if _, _ := r.Add(mk(3, "DEF")); r.Dups != 1 {
		t.Fatalf("dups=%d", r.Dups)
	}
	// First chunk arrives: both commit in order.
	segs, _ = r.Add(mk(0, "ABC"))
	if len(segs) != 2 || string(segs[0].Data) != "ABC" || string(segs[1].Data) != "DEF" {
		t.Fatalf("segs=%v", segs)
	}
	// Duplicate of an already-committed chunk.
	if _, _ := r.Add(mk(0, "ABC")); r.Dups != 2 {
		t.Fatalf("dups=%d", r.Dups)
	}
	if r.Committed() != 6 {
		t.Fatalf("committed=%d", r.Committed())
	}
}

func TestFirstMissingAndRetransmit(t *testing.T) {
	r := NewReassembler()
	mk := func(off int64, s string) Record {
		return Record{ArrivalNs: off + 1, FileOffset: uint64(off), Payload: []byte(s)}
	}
	r.Add(mk(0, "AB"))
	r.Add(mk(6, "GH")) // gap [2,6)
	start, end, ok := r.FirstMissing()
	if !ok || start != 2 || end != 6 {
		t.Fatalf("missing [%d,%d) ok=%v", start, end, ok)
	}
	err := r.MissingError()
	if !fault.Is(err, fault.ErrMissingSegment) {
		t.Fatalf("err=%v", err)
	}
	// Burst loss recovered by retransmission.
	r.Add(mk(2, "CD"))
	segs, _ := r.Add(mk(4, "EF"))
	if len(segs) != 3 {
		t.Fatalf("after retransmit segs=%v", segs)
	}
	if _, _, ok := r.FirstMissing(); ok {
		t.Fatal("gap should be closed")
	}
}

func TestAdaptiveWindow(t *testing.T) {
	r := NewReassembler()
	w0 := r.WaitWindowNs()
	// Slow arrivals: 100 bytes every second -> window must widen.
	for i := int64(0); i < 8; i++ {
		r.Add(Record{ArrivalNs: i * 1_000_000_000, FileOffset: uint64(i * 100), Payload: make([]byte, 100)})
	}
	if r.WaitWindowNs() <= w0 {
		t.Fatalf("window did not widen: %d -> %d", w0, r.WaitWindowNs())
	}
	// Fast arrivals: window must shrink back.
	r2 := NewReassembler()
	for i := int64(0); i < 8; i++ {
		r2.Add(Record{ArrivalNs: i * 1000, FileOffset: uint64(i * 100), Payload: make([]byte, 100)})
	}
	if r2.WaitWindowNs() != MinWindowNs {
		t.Fatalf("fast link window=%d want min %d", r2.WaitWindowNs(), MinWindowNs)
	}
}
