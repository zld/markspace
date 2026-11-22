package adapter

import (
	"math"
	"testing"

	"github.com/pion/rtp/v2"

	"mediacore/media"
)

func makePacket(ssrc uint32, seq uint16, ts uint32, payload byte) []byte {
	p := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 96, SequenceNumber: seq,
			Timestamp: ts, SSRC: ssrc, Marker: true,
		},
		Payload: []byte{payload},
	}
	raw, err := p.Marshal()
	if err != nil {
		panic(err)
	}
	return raw
}

func TestRTPInOrderEmitsImmediately(t *testing.T) {
	s, err := NewRTPStream(1, 48000, 16)
	if err != nil {
		t.Fatal(err)
	}
	var emitted int
	for i := uint16(0); i < 10; i++ {
		r := s.Push(makePacket(1, i, uint32(i)*960, byte(i)))
		if len(r.Events) != 1 {
			t.Fatalf("seq %d emitted %d events, want 1", i, len(r.Events))
		}
		emitted++
	}
	if emitted != 10 || s.CommittedTo() != 9 {
		t.Fatalf("emitted=%d committed=%d", emitted, s.CommittedTo())
	}
}

func TestRTPReorderWithinWindow(t *testing.T) {
	s, _ := NewRTPStream(1, 48000, 8)
	// feed 0,1 then drop 2 and send 3..6; 0,1 emit, then hole at 2
	s.Push(makePacket(1, 0, 0, 0))
	s.Push(makePacket(1, 1, 960, 1))
	for seq := uint16(3); seq <= 6; seq++ {
		s.Push(makePacket(1, seq, uint32(seq)*960, byte(seq)))
	}
	if s.CommittedTo() != 1 {
		t.Fatalf("stream should be blocked at seq 1 hole, committed=%d", s.CommittedTo())
	}
	// late arrival of 2 unblocks and releases everything up to 6
	r := s.Push(makePacket(1, 2, 1920, 2))
	if len(r.Events) != 5 {
		t.Fatalf("after hole fill emitted %d, want 5", len(r.Events))
	}
	for i, ev := range r.Events {
		if ev.SeqExpanded != int64(i+2) {
			t.Fatalf("event %d seq %d out of order", i, ev.SeqExpanded)
		}
	}
}

func TestRTPLateAndDuplicateDoNotRewriteCommitted(t *testing.T) {
	s, _ := NewRTPStream(1, 48000, 8)
	for i := uint16(0); i < 5; i++ {
		s.Push(makePacket(1, i, uint32(i)*960, byte(i)))
	}
	// a resend of an already committed sequence is a window-outside late
	// packet (it can never re-enter the committed region), not a duplicate
	r1 := s.Push(makePacket(1, 3, 3*960, 99))
	if r1.Kind != RTPLate {
		t.Fatalf("committed resend classified as %v, want late", r1.Kind)
	}
	// an earlier window-outside late packet
	r2 := s.Push(makePacket(1, 0, 0, 88))
	if r2.Kind != RTPLate || len(r2.Events) != 0 {
		t.Fatalf("late packet classified as %v", r2.Kind)
	}
	_, dup, late, _ := s.Counters()
	if dup != 0 || late != 2 {
		t.Fatalf("counters dup=%d late=%d", dup, late)
	}
	if s.CommittedTo() != 4 {
		t.Fatal("committed frontier was rewritten")
	}
}

func TestRTPBadPacketCounted(t *testing.T) {
	s, _ := NewRTPStream(1, 48000, 8)
	r := s.Push([]byte{0x00, 0x01, 0x02}) // too short / bad version
	if r.Kind != RTPBad {
		t.Fatalf("bad packet classified as %v", r.Kind)
	}
	_, _, _, bad := s.Counters()
	if bad != 1 {
		t.Fatalf("bad count %d", bad)
	}
}

func TestRTPDuplicateWhileBuffered(t *testing.T) {
	s, _ := NewRTPStream(1, 48000, 8)
	s.Push(makePacket(1, 0, 0, 0))
	s.Push(makePacket(1, 1, 960, 1))
	// seq 3..5 buffered behind the hole at seq 2
	for seq := uint16(3); seq <= 5; seq++ {
		s.Push(makePacket(1, seq, uint32(seq)*960, byte(seq)))
	}
	// resending a still-buffered sequence is a duplicate, not a late packet
	r := s.Push(makePacket(1, 4, 4*960, 77))
	if r.Kind != RTPDuplicate {
		t.Fatalf("buffered resend classified as %v, want duplicate", r.Kind)
	}
	_, dup, _, _ := s.Counters()
	if dup != 1 {
		t.Fatalf("dup counter %d", dup)
	}
}

func TestRTPSequenceAndTimestampWrap(t *testing.T) {
	s, _ := NewRTPStream(1, 48000, 8)
	base := uint32(math.MaxUint32) - 2*960
	seqs := []uint16{65534, 65535, 0, 1, 2}
	var lastTS int64
	var lastSeq int64 = -1
	for i, seq := range seqs {
		r := s.Push(makePacket(1, seq, base+uint32(i)*960, byte(i)))
		if len(r.Events) == 1 {
			ev := r.Events[0]
			if lastSeq >= 0 && ev.SeqExpanded != lastSeq+1 {
				t.Fatalf("expanded seq discontinuity at %d: %d after %d", i, ev.SeqExpanded, lastSeq)
			}
			if i > 0 && ev.TSExpanded <= lastTS {
				t.Fatalf("timestamp not monotonic across wrap at %d", i)
			}
			lastTS, lastSeq = ev.TSExpanded, ev.SeqExpanded
		}
	}
	// expanded sequences preserve absolute position: 65534..65538
	if s.CommittedTo() != 65538 {
		t.Fatalf("post-wrap committed seq = %d, want 65538", s.CommittedTo())
	}
}

func TestRTPHoleReportedAtWindowExpiry(t *testing.T) {
	s, _ := NewRTPStream(1, 48000, 4)
	s.Push(makePacket(1, 0, 0, 0))
	// seq 1 missing; fill window so the hole expires
	for seq := uint16(2); seq <= 5; seq++ {
		s.Push(makePacket(1, seq, uint32(seq)*960, byte(seq)))
	}
	holes := s.DrainHoles()
	if len(holes) != 1 || holes[0] != (media.Interval{Start: 1, End: 2}) {
		t.Fatalf("holes = %v", holes)
	}
}

func TestRTPCheckpointResumeDeterministic(t *testing.T) {
	s, _ := NewRTPStream(1, 48000, 8)
	// create a buffered hole (seq 2 missing) then checkpoint
	s.Push(makePacket(1, 0, 0, 0))
	s.Push(makePacket(1, 1, 960, 1))
	for seq := uint16(3); seq <= 5; seq++ {
		s.Push(makePacket(1, seq, uint32(seq)*960, byte(seq)))
	}
	snap, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreRTPStream(snap)
	if err != nil {
		t.Fatal(err)
	}
	// feeding the missing seq to both streams must yield identical events
	a := s.Push(makePacket(1, 2, 1920, 2))
	b := restored.Push(makePacket(1, 2, 1920, 2))
	if len(a.Events) != len(b.Events) {
		t.Fatalf("event count diverges %d vs %d", len(a.Events), len(b.Events))
	}
	for i := range a.Events {
		if a.Events[i].SeqExpanded != b.Events[i].SeqExpanded ||
			a.Events[i].TSExpanded != b.Events[i].TSExpanded ||
			a.Events[i].PTSNanos != b.Events[i].PTSNanos {
			t.Fatalf("event %d diverges after restore", i)
		}
	}
}

func TestRTPRejectsOversizedWindow(t *testing.T) {
	if _, err := NewRTPStream(1, 48000, media.MaxReorderWindow+1); err == nil {
		t.Fatal("window over limit accepted")
	}
}
