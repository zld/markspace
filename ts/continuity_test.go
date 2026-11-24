package ts

import (
	"testing"

	"mediad/internal/clock"
)

func ccPacketAt(pid uint16, cc uint8, ordinal int64) Packet {
	return Packet{PID: pid, CC: cc, Offset: ordinal * PacketSize}
}

func ccPacket(pid uint16, cc uint8) Packet {
	return ccPacketAt(pid, cc, int64(cc))
}

func TestContinuityHappyPath(t *testing.T) {
	tr := NewContinuityTracker()
	for i := 0; i < 20; i++ {
		if dup := tr.Feed(ccPacket(0x100, uint8(i%16)), int64(i)); dup {
			t.Fatalf("packet %d falsely flagged duplicate", i)
		}
	}
	if tr.Duplicates() != 0 || len(tr.Events) != 0 {
		t.Fatalf("clean stream must have no anomalies: %+v", tr.Events)
	}
}

func TestContinuityDuplicatesIgnored(t *testing.T) {
	tr := NewContinuityTracker()
	tr.Feed(ccPacket(0x100, 0), 0)
	tr.Feed(ccPacket(0x100, 1), 1)
	if dup := tr.Feed(ccPacket(0x100, 1), 2); !dup { // retransmit
		t.Fatal("same CC must be a duplicate")
	}
	if dup := tr.Feed(ccPacket(0x100, 1), 3); !dup { // second retransmit
		t.Fatal("repeated CC must stay a duplicate")
	}
	// Next in-order packet resumes from the last non-duplicate CC.
	if dup := tr.Feed(ccPacket(0x100, 2), 4); dup {
		t.Fatal("in-order packet after duplicates must not be duplicate")
	}
	if tr.Duplicates() != 2 || len(tr.Events) != 0 {
		t.Fatalf("duplicates=%d events=%+v, want 2 and none", tr.Duplicates(), tr.Events)
	}
}

func TestContinuityJumpRecordedWithRange(t *testing.T) {
	tr := NewContinuityTracker()
	tr.Feed(ccPacketAt(0x200, 3, 10), 10) // offset = 10*188
	tr.Feed(ccPacketAt(0x200, 7, 11), 11) // want 4, got 7; offset = 11*188
	if len(tr.Events) != 1 {
		t.Fatalf("want one jump event, got %d", len(tr.Events))
	}
	e := tr.Events[0]
	if e.PID != 0x200 || e.WantCC != 4 || e.GotCC != 7 || e.Kind != clock.KindContinuity {
		t.Fatalf("event content wrong: %+v", e)
	}
	if e.PacketRange != [2]int64{10, 12} {
		t.Fatalf("packet range = %v, want [10 12]", e.PacketRange)
	}
	if e.OffsetRange != [2]int64{10 * PacketSize, 12 * PacketSize} {
		t.Fatalf("offset range = %v, want [%d %d]", e.OffsetRange, 10*PacketSize, 12*PacketSize)
	}
	re := e.Report()
	if re.Kind != clock.KindContinuity || !re.Contains(10*PacketSize) || re.Contains(re.End) {
		t.Fatalf("structured report wrong: %+v", re)
	}
	// Per-PID state is independent.
	tr.Feed(ccPacketAt(0x300, 0, 0), 0)
	if len(tr.Events) != 1 {
		t.Fatal("a fresh PID must not produce a jump")
	}
}

func TestContinuityWrapAt16(t *testing.T) {
	tr := NewContinuityTracker()
	tr.Feed(ccPacket(0x100, 15), 0)
	if dup := tr.Feed(ccPacket(0x100, 0), 1); dup {
		t.Fatal("CC 15 -> 0 is a normal 4-bit wrap, not a duplicate")
	}
	if len(tr.Events) != 0 || tr.Duplicates() != 0 {
		t.Fatalf("wrap must be clean: events=%+v dup=%d", tr.Events, tr.Duplicates())
	}
}

func TestContinuityNullPIDIgnored(t *testing.T) {
	tr := NewContinuityTracker()
	for i := 0; i < 5; i++ {
		if dup := tr.Feed(ccPacket(PIDNull, uint8(i)), int64(i)); dup {
			t.Fatal("null PID packets are never duplicates")
		}
	}
	if len(tr.states) != 0 || len(tr.Events) != 0 {
		t.Fatal("null PID must not create tracking state")
	}
}
