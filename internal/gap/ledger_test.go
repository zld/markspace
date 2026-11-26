package gap

import (
	"testing"

	"mediacheck/internal/timebase"
)

func TestRecordMapsSamplesToTicks(t *testing.T) {
	l := NewLedger("cam-1", 48000)
	g, err := l.RecordMissing(65534, 65537, 3, 48000, 49440, "burst loss")
	if err != nil {
		t.Fatal(err)
	}
	if g.StartTick != int64(timebase.TickRate) || g.EndTick != int64(timebase.TickRate)+30_000_000 {
		t.Fatalf("ticks=[%d,%d)", g.StartTick, g.EndTick)
	}
	if l.Count() != 1 || l.MissingPackets() != 3 {
		t.Fatalf("count=%d packets=%d", l.Count(), l.MissingPackets())
	}
}

func TestRejectsReversedInterval(t *testing.T) {
	_, err := NewLedger("x", 48000).RecordLate(1, 10, -1, "bad")
	if !timebase.IsCode(err, timebase.CodeInvalidArgument) {
		t.Fatalf("err=%v", err)
	}
}
