package recovery

import (
	"testing"

	"mediarun/internal/fault"
	rtpparse "mediarun/internal/rtp"
)

// packet builds an audio packet at 20 ms spacing (960 ticks @ 48 kHz).
func packet(ext uint64, offset int64) *rtpparse.Packet {
	return &rtpparse.Packet{
		ExtSeq:    ext,
		Timestamp: uint32(ext * 960),
		Offset:    offset,
		Payload:   []byte{byte(ext)},
	}
}

func TestRecoverFillsSmallGap(t *testing.T) {
	ordered := []*rtpparse.Packet{packet(1, 0), packet(2, 100), packet(5, 250)}
	rep, err := Recover(ordered, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Recovered != 2 || rep.Incomplete {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.Units) != 5 {
		t.Fatalf("units = %d", len(rep.Units))
	}
	if !rep.Units[2].Gap || !rep.Units[3].Gap {
		t.Fatal("recovered slots not marked as gaps")
	}
	if rep.Units[2].Packet != nil {
		t.Fatal("recovery fabricated a packet")
	}
	if rep.Units[4].Packet == nil {
		t.Fatal("real packet lost")
	}
	if rep.LengthTicks() != 5*960 {
		t.Fatalf("length = %d", rep.LengthTicks())
	}
}

func TestRecoverMarksLargeGapIncomplete(t *testing.T) {
	ordered := []*rtpparse.Packet{packet(1, 0), packet(20, 100)}
	rep, err := Recover(ordered, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Incomplete {
		t.Fatal("large gap not marked incomplete")
	}
	if rep.Recovered != 0 {
		t.Fatalf("recovered = %d", rep.Recovered)
	}
	if rep.Unrecovered != 18 {
		t.Fatalf("unrecovered = %d", rep.Unrecovered)
	}
	if rep.Faults.CountKind(fault.KindLoss) != 1 || rep.Faults.CountKind(fault.KindIncomplete) != 1 {
		t.Fatalf("faults = %s", rep.Faults.Summary())
	}
	if rep.Faults[0].Offset != 0 {
		t.Fatalf("fault offset = %d", rep.Faults[0].Offset)
	}
}

func TestRecoverBoundaryGapIsRecoverable(t *testing.T) {
	// Exactly MaxGapPackets missing packets is still recoverable.
	ordered := []*rtpparse.Packet{packet(1, 0), packet(5, 0)}
	rep, _ := Recover(ordered, DefaultPolicy())
	if rep.Incomplete || rep.Recovered != 3 {
		t.Fatalf("report = %+v", rep)
	}
	// One more missing packet crosses the threshold.
	rep, _ = Recover([]*rtpparse.Packet{packet(1, 0), packet(6, 0)}, DefaultPolicy())
	if !rep.Incomplete || rep.Recovered != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestRecoverNoGap(t *testing.T) {
	rep, err := Recover([]*rtpparse.Packet{packet(1, 0), packet(2, 0), packet(3, 0)}, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if rep.GapEvents != 0 || rep.Recovered != 0 || rep.Incomplete {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.Units) != 3 {
		t.Fatalf("units = %d", len(rep.Units))
	}
}

func TestRecoverEmptyAndBadPolicy(t *testing.T) {
	rep, err := Recover(nil, DefaultPolicy())
	if err != nil || len(rep.Units) != 0 {
		t.Fatalf("empty: %v %+v", err, rep)
	}
	if _, err := Recover([]*rtpparse.Packet{packet(1, 0)}, Policy{NominalTickSpan: 0}); err == nil {
		t.Fatal("bad policy accepted")
	}
}

func TestRecoverCustomTimestampSpan(t *testing.T) {
	// 10 ms frames (480 ticks) with one loss: each slot spans 480 ticks.
	a := &rtpparse.Packet{ExtSeq: 1, Timestamp: 0}
	b := &rtpparse.Packet{ExtSeq: 3, Timestamp: 960}
	rep, _ := Recover([]*rtpparse.Packet{a, b}, Policy{MaxGapPackets: 3, MaxGapEvents: 4, NominalTickSpan: 480})
	if rep.Units[1].TickSpan != 480 || rep.Units[1].RTPTimestamp != 480 {
		t.Fatalf("unit = %+v", rep.Units[1])
	}
}
