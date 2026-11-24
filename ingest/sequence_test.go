package ingest

import (
	"testing"
)

func TestTrackerWrapReorderDuplicateAndGap(t *testing.T) {
	tr := NewTracker()

	// Observe 65534, 65535, then wrap to 0 and 1.
	input := []uint16{65534, 65535, 1, 0, 1, 3}
	wantExt := []uint64{65534, 65535, 65537, 65536, 65537, 65539}
	wantDisp := []Disposition{
		DispositionAccepted,
		DispositionAccepted,
		DispositionAccepted, // creates hole 65536
		DispositionOutOfOrder,
		DispositionDuplicate,
		DispositionAccepted, // creates hole 65538
	}
	for i, seq := range input {
		got := tr.Classify(seq)
		if got.Extended != wantExt[i] {
			t.Fatalf("input %d seq=%d: ext=%d want %d", i, seq, got.Extended, wantExt[i])
		}
		if got.Disposition != wantDisp[i] {
			t.Fatalf("input %d seq=%d: disposition=%s want %s", i, seq, got.Disposition, wantDisp[i])
		}
	}
	if tr.Duplicates != 1 {
		t.Fatalf("duplicates=%d want 1", tr.Duplicates)
	}
	missing := tr.Missing()
	if len(missing) != 1 || missing[0] != 65538 {
		t.Fatalf("missing=%v want [65538]", missing)
	}
	gaps := tr.Gaps()
	if len(gaps) != 1 || gaps[0].First != 65538 || gaps[0].Count != 1 {
		t.Fatalf("gaps=%+v want one gap at 65538", gaps)
	}
}

func TestCollectorDoesNotRetainDuplicate(t *testing.T) {
	c := NewCollector("test")
	raw, err := BuildRTP(7, 700, []byte("opus"), true)
	if err != nil {
		t.Fatal(err)
	}
	if r := c.Ingest(raw); r.Packet == nil {
		t.Fatalf("first ingest returned no packet: %+v", r)
	}
	if r := c.Ingest(raw); r.Packet != nil {
		t.Fatalf("duplicate produced retained packet: %+v", r)
	}
	if len(c.Ordered()) != 1 {
		t.Fatalf("ordered length=%d want 1", len(c.Ordered()))
	}
	if c.DuplicateCount() != 1 {
		t.Fatalf("duplicates=%d want 1", c.DuplicateCount())
	}
}

func TestParseRTPRejectsTruncatedPacket(t *testing.T) {
	_, err := ParseRTP([]byte{0x80, 0x60, 0x00})
	if err == nil {
		t.Fatal("expected truncated RTP error")
	}
}
