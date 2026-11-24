package ts

import (
	"testing"

	"mediarun/internal/clock"
)

func payloadPkt(pid uint16, cc uint8) *Packet {
	return &Packet{PID: pid, CC: cc, HasPayload: true, Offset: int64(cc) * 188}
}

func TestContinuity_NormalSequence(t *testing.T) {
	c := NewContinuityChecker(map[uint16]string{0x100: "audio"})
	rec := &clock.Recorder{}
	for cc := uint8(0); cc < 20; cc++ {
		if c.Observe(payloadPkt(0x100, cc&0x0f), rec) {
			t.Fatalf("cc=%d falsely flagged duplicate", cc)
		}
	}
	if rec.CountByKind()["cc_gap"] != 0 || c.TotalGap() != 0 {
		t.Fatalf("clean sequence produced events: %v", rec.Events())
	}
}

func TestContinuity_DuplicateAndGap(t *testing.T) {
	c := NewContinuityChecker(map[uint16]string{0x100: "audio"})
	rec := &clock.Recorder{}
	c.Observe(payloadPkt(0x100, 0), rec)
	c.Observe(payloadPkt(0x100, 1), rec)
	if !c.Observe(payloadPkt(0x100, 1), rec) {
		t.Fatal("repeated cc must be flagged duplicate")
	}
	c.Observe(payloadPkt(0x100, 4), rec) // gap: 2,3 missing
	counts := rec.CountByKind()
	if counts["cc_dup"] != 1 || counts["cc_gap"] != 1 {
		t.Fatalf("counts = %v", counts)
	}
	if c.TotalGap() != 2 {
		t.Fatalf("total gap = %d, want 2", c.TotalGap())
	}
	ev := rec.Events()[1]
	if ev.PID != 0x100 || ev.Track != "audio" || ev.Range.Start != 4*188 {
		t.Fatalf("event annotation wrong: %+v", ev)
	}
}

func TestContinuity_CounterWrap(t *testing.T) {
	c := NewContinuityChecker(nil)
	rec := &clock.Recorder{}
	c.Observe(payloadPkt(0x200, 14), rec)
	c.Observe(payloadPkt(0x200, 15), rec)
	c.Observe(payloadPkt(0x200, 0), rec) // natural 4-bit wrap, not a gap
	if c.TotalGap() != 0 {
		t.Fatalf("counter wrap misreported as gap: %v", rec.Events())
	}
}

func TestContinuity_NoPayloadIgnored(t *testing.T) {
	c := NewContinuityChecker(nil)
	rec := &clock.Recorder{}
	p := &Packet{PID: 0x200, CC: 5, HasPayload: false}
	if c.Observe(p, rec) || len(rec.Events()) != 0 {
		t.Fatal("adaptation-only packets must not touch CC state")
	}
}
