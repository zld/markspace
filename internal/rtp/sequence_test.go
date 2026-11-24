package rtp

import (
	"bytes"
	"testing"

	pion "github.com/pion/rtp"
)

func TestExtenderWrapForward(t *testing.T) {
	e, err := NewExtender(16)
	if err != nil {
		t.Fatal(err)
	}
	seq := []uint16{65533, 65534, 65535, 0, 1, 2}
	var got []int64
	for _, s := range seq {
		ext, backward := e.Extend(uint64(s))
		if backward {
			t.Errorf("seq %d reported backward", s)
		}
		got = append(got, ext)
	}
	for i := 1; i < len(got); i++ {
		if got[i] != got[i-1]+1 {
			t.Fatalf("extension not contiguous across wrap: %v", got)
		}
	}
}

func TestExtenderBackwardAcrossWrap(t *testing.T) {
	e, _ := NewExtender(16)
	// Establish a reference just past the wrap, then deliver a late packet
	// from before it.
	e.Extend(2)
	ext, backward := e.Extend(65535)
	if !backward {
		t.Fatalf("expected backward, ext=%d", ext)
	}
	if ext != 65535-65536 {
		t.Errorf("ext = %d, want %d", ext, 65535-65536)
	}
}

func TestExtenderMultipleWraps(t *testing.T) {
	e, _ := NewExtender(8)
	var prev int64
	for i := 0; i < 600; i++ {
		ext, _ := e.Extend(uint64(i % 256))
		if i == 0 {
			prev = ext
			continue
		}
		if ext != prev+1 {
			t.Fatalf("step %d: ext=%d prev=%d", i, ext, prev)
		}
		prev = ext
	}
}

func TestExtenderRejectsBadWidth(t *testing.T) {
	if _, err := NewExtender(0); err == nil {
		t.Error("width 0 must be rejected")
	}
	if _, err := NewExtender(64); err == nil {
		t.Error("width 64 must be rejected")
	}
}

func TestDeduperCountsDuplicateOnce(t *testing.T) {
	d, err := NewDeduper(16, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range []uint16{100, 101, 102, 101, 102, 103} {
		seq := d.Ingest(Header{Seq: s, Arrival: int64(i)})
		wantDup := i == 3 || i == 4
		if seq.Duplicate != wantDup {
			t.Errorf("step %d seq %d duplicate=%v want %v", i, s, seq.Duplicate, wantDup)
		}
	}
	if d.Count() != 4 {
		t.Errorf("Count = %d, want 4 distinct", d.Count())
	}
}

func TestDeduperLateWindow(t *testing.T) {
	d, _ := NewDeduper(16, 4)
	d.Ingest(Header{Seq: 10})
	d.Ingest(Header{Seq: 11})
	d.Ingest(Header{Seq: 12})
	d.Ingest(Header{Seq: 13})
	d.Ingest(Header{Seq: 14})
	d.Ingest(Header{Seq: 15})
	// Sequence 9 is five behind the high water mark, outside a window of 4.
	s := d.Ingest(Header{Seq: 9})
	if !s.Late {
		t.Fatalf("expected late, LateBy=%d", s.LateBy)
	}
	if s.LateBy != 6 {
		t.Errorf("LateBy = %d, want 6", s.LateBy)
	}
}

func TestDeduperSnapshotRoundTrip(t *testing.T) {
	d, _ := NewDeduper(16, 0)
	for _, s := range []uint16{65533, 65534, 65535, 0, 1} {
		d.Ingest(Header{Seq: s})
	}
	snap := d.Snapshot()
	d2, _ := NewDeduper(16, 0)
	d2.Restore(snap)
	if got := d2.Count(); got != d.Count() {
		t.Errorf("restored count = %d, want %d", got, d.Count())
	}
	if ext := d2.Ingest(Header{Seq: 2}).Ext; ext != 65538 {
		t.Errorf("restored extender continued at %d, want 65538", ext)
	}
	if !d2.Ingest(Header{Seq: 0}).Duplicate {
		t.Error("restored deduper lost a seen sequence")
	}
}

func TestRelativeExt(t *testing.T) {
	d, _ := NewDeduper(16, 0)
	d.Ingest(Header{Seq: 65535})
	d.Ingest(Header{Seq: 0})
	d.Ingest(Header{Seq: 1})
	// The extender is anchored at its first value (65535), so 65534 extends to
	// 65534 and 0 to 65536.
	if got := d.RelativeExt(65534); got != 65534 {
		t.Errorf("RelativeExt(65534) = %d, want 65534", got)
	}
	if got := d.RelativeExt(0); got != 65536 {
		t.Errorf("RelativeExt(0) = %d, want 65536", got)
	}
}

func TestTimestampExtender(t *testing.T) {
	te, err := NewTimestampExtender(32, 90_000)
	if err != nil {
		t.Fatal(err)
	}
	ext, ts, backward, err := te.ExtendNanos(0xFFFFFFFF)
	if err != nil {
		t.Fatal(err)
	}
	if backward {
		t.Error("first sample reported backward")
	}
	if ts <= 0 {
		t.Errorf("ts = %d", ts)
	}
	_, ts2, _, err := te.ExtendNanos(0)
	if err != nil {
		t.Fatal(err)
	}
	if ts2 <= ts || ts2-ts > 1e6 {
		t.Errorf("wrap conversion looks wrong: ts=%d ts2=%d ext=%d", ts, ts2, ext)
	}
}

func TestParseRTPPacket(t *testing.T) {
	p := &pion.Packet{Header: pion.Header{
		Version: 2, PayloadType: 96, SequenceNumber: 4242,
		Timestamp: 9000, SSRC: 7,
	}, Payload: []byte{1, 2, 3, 4}}
	raw, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	h, err := Parse(3, 11, raw, "cap.ndjson", 0)
	if err != nil {
		t.Fatal(err)
	}
	if h.Seq != 4242 || h.Timestamp != 9000 || h.SSRC != 7 || h.Size != 4 || h.Arrival != 11 {
		t.Errorf("parsed header = %+v", h)
	}
}

func TestParseRejectsShortPacket(t *testing.T) {
	_, err := Parse(1, 0, []byte{0x80, 0x60}, "cap.ndjson", 12)
	if err == nil {
		t.Fatal("expected error for short packet")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("offset=12")) {
		t.Errorf("error must carry the byte offset: %v", err)
	}
}

func TestSortOrders(t *testing.T) {
	seqs := []Sequence{
		{Ext: 3, Header: Header{Arrival: 1}},
		{Ext: 1, Header: Header{Arrival: 9}},
		{Ext: 1, Header: Header{Arrival: 2}},
	}
	SortOrders(seqs)
	if seqs[0].Ext != 1 || seqs[0].Header.Arrival != 2 || seqs[1].Header.Arrival != 9 {
		t.Errorf("unexpected order: %+v", seqs)
	}
}
