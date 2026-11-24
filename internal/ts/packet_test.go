package ts_test

import (
	"errors"
	"testing"

	"medpipe/internal/fault"
	"medpipe/internal/samplegen"
	"medpipe/internal/ts"
)

func cleanTS() []byte {
	spec := samplegen.MediaSpec(17)
	return samplegen.BuildTS(spec)
}

func TestReadCleanPacketsAndContinuity(t *testing.T) {
	data := cleanTS()
	res, err := ts.ReadPackets(data)
	if err != nil {
		t.Fatalf("ReadPackets: %v", err)
	}
	if len(res.Packets) == 0 {
		t.Fatal("no packets")
	}
	for _, e := range res.Events {
		if fault.IsGapCode(e.Code) {
			t.Fatalf("unexpected gap event in clean stream: %+v", e)
		}
	}
}

func TestPacketValidationErrors(t *testing.T) {
	valid := cleanTS()[:ts.PacketSize]
	pkt, err := ts.ParsePacket(valid, 0)
	if err != nil {
		t.Fatalf("valid packet: %v", err)
	}
	if pkt.PID != 0 {
		t.Fatalf("first PID=%d want PAT 0", pkt.PID)
	}

	badSync := append([]byte(nil), valid...)
	badSync[0] = 0x00
	if _, err := ts.ParsePacket(badSync, 0); err == nil || err.Code != fault.ErrSyncByte {
		t.Fatalf("sync error=%v", err)
	}

	if _, err := ts.ParsePacket(valid[:100], 0); err == nil || err.Code != fault.ErrPacketLength {
		t.Fatalf("length error=%v", err)
	}

	badAFC := make([]byte, ts.PacketSize)
	badAFC[0] = ts.SyncByte
	badAFC[1] = 0x00
	badAFC[2] = 0x00
	badAFC[3] = 0x00 // AFC=00 is reserved
	if _, err := ts.ParsePacket(badAFC, 7); err == nil || err.Code != fault.ErrAdaptation {
		t.Fatalf("adaptation error=%v", err)
	} else if err.Offset != 7 {
		t.Fatalf("offset=%d want 7", err.Offset)
	}
}

func TestContinuityJumpAndTruncationEvents(t *testing.T) {
	data := cleanTS()
	if _, err := samplegen.ApplyCCJump(data, samplegen.VideoPID, 1); err != nil {
		t.Fatal(err)
	}
	res, _ := ts.ReadPackets(data)
	var ccGap *fault.Event
	for i := range res.Events {
		if res.Events[i].Code == fault.ErrContinuity {
			ccGap = &res.Events[i]
		}
	}
	if ccGap == nil {
		t.Fatalf("missing CC gap event: %+v", res.Events)
	}
	if ccGap.PID != samplegen.VideoPID || ccGap.Offset < 0 {
		t.Fatalf("bad CC event location: %+v", ccGap)
	}

	truncated := samplegen.Truncate(data, 37)
	res2, err := ts.ReadPackets(truncated)
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Code != fault.ErrTruncated {
		t.Fatalf("error=%v want truncation", err)
	}
	found := false
	for _, e := range res2.Events {
		if e.Code == fault.ErrTruncated {
			found = true
		}
	}
	if !found {
		t.Fatal("truncation event not retained")
	}
}

func TestPCRParsing(t *testing.T) {
	spec := samplegen.TSSpec{VideoPID: 0x100, IncludePCR: true}
	spec.Video = []samplegen.SampleSpec{{PTS: 123456, Data: []byte("compressed")}}
	data := samplegen.BuildTS(spec)
	res, err := ts.ReadPackets(data)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range res.Packets {
		if p.Adaptation != nil && p.Adaptation.HasPCR {
			found = true
			if p.Adaptation.PCRBase != 123456 {
				t.Fatalf("PCR base=%d want 123456", p.Adaptation.PCRBase)
			}
		}
	}
	if !found {
		t.Fatal("no PCR parsed")
	}
}
