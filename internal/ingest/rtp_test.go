package ingest

import (
	"testing"

	"github.com/pion/rtp"

	"replay/internal/fault"
)

// buildRTP serialises a packet with the given sequence and timestamp.
func buildRTP(t *testing.T, seq uint16, ts uint32, ssrc uint32, payload []byte) []byte {
	t.Helper()
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           ssrc,
			PayloadType:    111,
		},
		Payload: payload,
	}
	raw, err := pkt.Marshal()
	if err != nil {
		t.Fatalf("marshal rtp: %v", err)
	}
	return raw
}

func TestParserReordersAndDeduplicates(t *testing.T) {
	p, err := NewParser("audio", 48000)
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	payload := func(s string) []byte { return []byte(s) }
	// Deliberately out of order, with one duplicate and one gap at 12.
	for _, seq := range []uint16{10, 11, 13, 11, 14} {
		raw := buildRTP(t, seq, uint32(seq)*320, 0xAABBCCDD, payload("frame-"+itoa(int(seq))))
		if err := p.Add(raw, int64(seq)*100); err != nil {
			t.Fatalf("Add seq %d: %v", seq, err)
		}
	}
	streams := p.Finish()
	if len(streams) != 1 {
		t.Fatalf("streams=%d want 1", len(streams))
	}
	st := streams[0]
	if st.PacketCount() != 4 {
		t.Fatalf("packets=%d want 4", st.PacketCount())
	}
	// Accepted slots are 10, 11, 13, 14; slot 12 is a deficit.
	wantExt := []uint64{10, 11, 13, 14}
	for i, want := range wantExt {
		if st.Packets[i].Extended != want {
			t.Fatalf("packet %d ext=%d want %d", i, st.Packets[i].Extended, want)
		}
	}
	if st.DuplicateCount() != 1 {
		t.Fatalf("duplicates=%d want 1", st.DuplicateCount())
	}
	if st.Deficits[0].Extended != 12 || st.Deficits[0].Reason != ReasonGap {
		t.Fatalf("deficit=%+v", st.Deficits[0])
	}
	if len(st.Deficits) != 1 {
		t.Fatalf("deficits=%d want 1", len(st.Deficits))
	}
}

func TestParserDetectsSequenceWrap(t *testing.T) {
	p, _ := NewParser("audio", 48000)
	seqs := []uint16{65533, 65534, 65535, 0, 1, 2}
	for i, seq := range seqs {
		if err := p.Add(buildRTP(t, seq, uint32(i)*960, 1, []byte("x")), int64(i)*10); err != nil {
			t.Fatalf("Add %d: %v", seq, err)
		}
	}
	st := p.Finish()[0]
	if st.Wraps != 1 {
		t.Fatalf("wraps=%d want 1", st.Wraps)
	}
	if st.PacketCount() != 6 {
		t.Fatalf("packets=%d", st.PacketCount())
	}
	// Extended slots must be strictly increasing across the wrap.
	for i := 1; i < st.PacketCount(); i++ {
		if st.Packets[i].Extended <= st.Packets[i-1].Extended {
			t.Fatalf("not ordered at %d: %d <= %d", i, st.Packets[i].Extended, st.Packets[i-1].Extended)
		}
	}
	if st.Packets[3].Extended != 65536 {
		t.Fatalf("post-wrap slot=%d want 65536", st.Packets[3].Extended)
	}
}

func TestParserLatePacketBecomesDeficit(t *testing.T) {
	p, _ := NewParserWindow("audio", 48000, 4)
	for _, seq := range []uint16{1, 2, 20} {
		if err := p.Add(buildRTP(t, seq, 0, 1, []byte("x")), 0); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	// seq 3 is now far outside the 4-slot window.
	if err := p.Add(buildRTP(t, 3, 0, 1, []byte("x")), 0); err != nil {
		t.Fatalf("late Add: %v", err)
	}
	st := p.Finish()[0]
	if st.LateCount() != 1 {
		t.Fatalf("late=%d want 1", st.LateCount())
	}
	// The late packet must not appear among accepted packets.
	for _, pkt := range st.Packets {
		if pkt.Seq == 3 && pkt.Extended == 3 {
			t.Fatal("late packet was inserted retroactively")
		}
	}
	var found bool
	for _, d := range st.Deficits {
		if d.Extended == 3 && d.Reason == ReasonLate {
			found = true
		}
	}
	if !found {
		t.Fatal("late deficit not recorded")
	}
}

func TestParserDuplicateWithDifferentPayloadIsConflict(t *testing.T) {
	p, _ := NewParser("audio", 48000)
	if err := p.Add(buildRTP(t, 1, 0, 1, []byte("one")), 0); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := p.Add(buildRTP(t, 1, 0, 1, []byte("different")), 4); err != nil {
		t.Fatalf("duplicate conflict must be recorded, not fatal: %v", err)
	}
	st := p.Finish()[0]
	if st.PacketCount() != 1 {
		t.Fatalf("packets=%d want 1 (duplicate counted once)", st.PacketCount())
	}
	conflicts := st.DuplicateConflicts()
	if len(conflicts) != 1 || conflicts[0].SamePayload {
		t.Fatalf("conflicts=%+v", conflicts)
	}
}

func TestParserMalformedPacketReportsOffset(t *testing.T) {
	p, _ := NewParser("audio", 48000)
	err := p.Add([]byte{0x80, 0x00, 0x00}, 41)
	if err == nil {
		t.Fatal("expected error")
	}
	fe, ok := fault.As(err)
	if !ok || !fe.HasOffset || fe.ByteOffset != 41 {
		t.Fatalf("missing offset: %v", err)
	}
}

func TestParserRejectsEmptyPayload(t *testing.T) {
	p, _ := NewParser("audio", 48000)
	if err := p.Add(buildRTP(t, 1, 0, 1, nil), 0); err == nil {
		t.Fatal("expected empty payload error")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
