package rtp

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"github.com/pion/rtp"

	"rtpregress/internal/clock"
)

func mkRecord(seq uint16, idx int64) *Record {
	p := &rtp.Packet{
		Header:  rtp.Header{SequenceNumber: seq, Timestamp: uint32(seq) * 3000, PayloadType: 33},
		Payload: []byte{0x47, byte(seq), 0x01},
	}
	return &Record{Packet: p, Index: idx, Offset: idx * 20}
}

func seqs(evs []Event) []uint16 {
	var out []uint16
	for _, e := range evs {
		out = append(out, e.Seq)
	}
	return out
}

func TestInOrderEmitsImmediately(t *testing.T) {
	b := NewReorderBuffer()
	var got []Event
	for s := uint16(0); s < 5; s++ {
		got = append(got, b.Push(mkRecord(s, int64(s)))...)
	}
	if want := []uint16{0, 1, 2, 3, 4}; !equalU16(seqs(got), want) {
		t.Fatalf("got %v want %v", seqs(got), want)
	}
	st := b.Stats()
	if st.InOrder != 5 || st.Lost != 0 || st.Duplicates != 0 {
		t.Fatalf("bad stats %+v", st)
	}
}

func TestReorderWithinWindow(t *testing.T) {
	b := NewReorderBuffer()
	b.Push(mkRecord(0, 0)) // base=0, emitted, base advances to 1
	if ev := b.Push(mkRecord(2, 1)); len(ev) != 0 {
		t.Fatalf("seq 2 must wait for the hole at 1, got %v", ev)
	}
	ev := b.Push(mkRecord(1, 2))
	if want := []uint16{1, 2}; !equalU16(seqs(ev), want) {
		t.Fatalf("got %v want %v", seqs(ev), want)
	}
}

func TestDuplicateCountedAndDropped(t *testing.T) {
	b := NewReorderBuffer()
	b.Push(mkRecord(0, 0)) // emitted, base -> 1
	b.Push(mkRecord(3, 1)) // buffered behind the hole at 1..2
	// same seq re-arrives while still inside the window: duplicate
	if ev := b.Push(mkRecord(3, 2)); len(ev) != 0 {
		t.Fatalf("duplicate must not emit, got %v", ev)
	}
	if b.Stats().Duplicates != 1 {
		t.Fatalf("duplicates = %d", b.Stats().Duplicates)
	}
	if b.Stats().InOrder != 1 {
		t.Fatalf("only seq 0 emitted so far, got %d", b.Stats().InOrder)
	}
}

func TestLateOutsideWindowMarkedLost(t *testing.T) {
	b := NewReorderBuffer()
	// establish base 0 then jump 70 ahead, forcing the window past seq 0..69
	ev := b.Push(mkRecord(0, 0))
	ev = append(ev, b.Push(mkRecord(70, 1))...)
	lost, pkts := countKinds(ev)
	if lost != 69 || pkts != 2 {
		t.Fatalf("want 69 lost and 2 packets, got lost=%d pkts=%d", lost, pkts)
	}
	// a packet for an abandoned sequence arrives late: counted, dropped
	b.Push(mkRecord(5, 2))
	if b.Stats().Late != 1 {
		t.Fatalf("late = %d, want 1", b.Stats().Late)
	}
	if b.Stats().Lost != 69 {
		t.Fatalf("lost = %d, want 69", b.Stats().Lost)
	}
}

func TestGapFilledBeforeFlush(t *testing.T) {
	b := NewReorderBuffer()
	got := b.Push(mkRecord(0, 0))                   // emitted, base -> 1
	if ev := b.Push(mkRecord(2, 1)); len(ev) != 0 { // hole at 1
		t.Fatalf("seq 2 must wait, got %v", ev)
	}
	got = append(got, b.Push(mkRecord(1, 2))...) // hole fills: 1 then 2
	if len(got) != 3 {
		t.Fatalf("want 3 total emits, got %d", len(got))
	}
	if b.Stats().Lost != 0 {
		t.Fatalf("filled gap must not be lost: %+v", b.Stats())
	}
}

func TestFlushReportsTrailingLoss(t *testing.T) {
	b := NewReorderBuffer()
	b.Push(mkRecord(10, 0)) // base -> 11
	b.Push(mkRecord(12, 1)) // buffered behind hole at 11
	ev := b.Flush()
	lost, pkts := countKinds(ev)
	if lost != 1 || pkts != 1 {
		t.Fatalf("flush: lost=%d pkts=%d", lost, pkts)
	}
	if ev[0].Kind != EventLost || ev[0].Seq != 11 || ev[1].Seq != 12 {
		t.Fatalf("flush order wrong: %+v", ev)
	}
	if ev[0].Record != nil {
		t.Fatal("lost events must not carry fabricated audio")
	}
}

func TestSequenceWrapAt65535(t *testing.T) {
	b := NewReorderBuffer()
	ev := b.Push(mkRecord(65535, 0))
	ev = append(ev, b.Push(mkRecord(0, 1))...)
	if want := []uint16{65535, 0}; !equalU16(seqs(ev), want) {
		t.Fatalf("wrap got %v want %v", seqs(ev), want)
	}
	// re-push of 65535 after the base moved to 1: outside window -> late
	if ev := b.Push(mkRecord(65535, 2)); len(ev) != 0 || b.Stats().Late != 1 {
		t.Fatalf("post-wrap re-push must count as late: ev=%d late=%d",
			len(ev), b.Stats().Late)
	}
}

func countKinds(evs []Event) (lost, pkts int) {
	for _, e := range evs {
		if e.Kind == EventLost {
			lost++
		} else {
			pkts++
		}
	}
	return
}

func equalU16(a, b []uint16) bool {
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

func TestTraceReaderRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(Magic[:])
	for s := uint16(0); s < 4; s++ {
		raw, _ := mkRecord(s, int64(s)).Packet.Marshal()
		var lenBuf [4]byte
		lenBuf[0] = byte(len(raw) >> 24)
		lenBuf[1] = byte(len(raw) >> 16)
		lenBuf[2] = byte(len(raw) >> 8)
		lenBuf[3] = byte(len(raw))
		buf.Write(lenBuf[:])
		buf.Write(raw)
	}
	recs, err := ReadAll(context.Background(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 4 || recs[3].Index != 3 || recs[3].Packet.SequenceNumber != 3 {
		t.Fatalf("round trip failed: %+v", recs)
	}
}

func TestTraceReaderRejectsBadMagicAndOversize(t *testing.T) {
	bad := bytes.NewBufferString("XXXX")
	if _, err := ReadAll(context.Background(), bad); err == nil {
		t.Fatal("bad magic must error")
	}
	big := bytes.NewBuffer(Magic[:])
	big.Write([]byte{0x00, 0x00, 0x10, 0x01}) // length 4097 > MaxRecordLen
	if _, err := ReadAll(context.Background(), big); err == nil {
		t.Fatal("oversize record must error")
	} else if e := clock.AsError(err); e == nil || e.Code != clock.CodeLimitExceeded {
		t.Fatalf("want CodeLimitExceeded, got %v", err)
	}
}

func TestTraceReaderTruncatedBody(t *testing.T) {
	data := append([]byte{}, Magic[:]...)
	data = append(data, 0, 0, 0, 10) // claims 10 bytes
	data = append(data, 0x80)        // only 1 present
	// a truncated tail is recoverable: no error, but a flagged offset
	recs, off, err := ReadAllWithTruncation(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("truncation must be recoverable, got %v", err)
	}
	if len(recs) != 0 || off < 0 {
		t.Fatalf("want zero records and a truncation offset, got n=%d off=%d",
			len(recs), off)
	}
	// the low-level reader still surfaces the explicit sentinel
	rd := NewReader(bytes.NewReader(data))
	if _, rerr := rd.Read(); rerr != ErrTraceTruncated {
		t.Fatalf("reader sentinel = %v, want ErrTraceTruncated", rerr)
	}
}

func TestTraceReaderTruncationKeepsCompletedRecords(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(Magic[:])
	// one complete record, then a truncated length prefix
	raw, _ := mkRecord(0, 0).Packet.Marshal()
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(raw)))
	buf.Write(lenBuf[:])
	buf.Write(raw)
	buf.Write([]byte{0, 0}) // partial next length prefix
	recs, _, err := ReadAllWithTruncation(context.Background(), &buf)
	if err != nil || len(recs) != 1 {
		t.Fatalf("complete record must survive truncation: n=%d err=%v",
			len(recs), err)
	}
}
