package kernel

import "testing"

func TestTSParserCCJumpAndPCRWrap(t *testing.T) {
	p := NewTSParser()
	const pid = 0x0100
	start := PCRBasePeriod - 2*ClockHz
	pcr0 := start
	pcr1 := uint64(ClockHz) // one second after wrap: start + 3s mod 2^33? start=P-2s, then P+1s

	p0, err := BuildTSPacket(pid, 0, []byte("one"), true, &pcr0)
	if err != nil {
		t.Fatal(err)
	}
	p1, err := BuildTSPacket(pid, 2, []byte("two"), true, &pcr1)
	if err != nil {
		t.Fatal(err)
	}
	if _, gaps, err := p.ParseDatagram(p0, 1); err != nil || len(gaps) != 0 {
		t.Fatalf("first packet gaps=%#v err=%v", gaps, err)
	}
	packets, gaps, err := p.ParseDatagram(p1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(packets) != 1 || packets[0].PCR == nil {
		t.Fatalf("packet parse failed: %#v", packets)
	}
	if len(gaps) != 1 || gaps[0].Kind != GapCCJump || gaps[0].PID != pid {
		t.Fatalf("expected one CC gap, got %#v", gaps)
	}
	if p.ClockState().Wraps != 1 {
		t.Fatalf("wraps=%d want 1", p.ClockState().Wraps)
	}
	if gaps[0].Time.End90k <= gaps[0].Time.Start90k {
		t.Fatalf("gap has invalid time range: %#v", gaps[0].Time)
	}
}

func TestTSParserTruncationGap(t *testing.T) {
	p := NewTSParser()
	_, gaps, err := p.ParseDatagram(make([]byte, 100), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 1 || gaps[0].Kind != GapTruncatedTS {
		t.Fatalf("gaps=%#v", gaps)
	}
}

func TestTSParserSyncLossGap(t *testing.T) {
	raw, err := BuildTSPacket(0x0102, 0, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw[0] = 0x00
	p := NewTSParser()
	packets, gaps, err := p.ParseDatagram(raw, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(packets) != 0 {
		t.Fatalf("unexpected packets after sync loss: %d", len(packets))
	}
	if len(gaps) != 1 || gaps[0].Kind != GapSyncLoss {
		t.Fatalf("gaps=%#v", gaps)
	}
}
