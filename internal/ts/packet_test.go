package ts

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"tsrepair/internal/mediaerr"
)

// buildPacket assures a minimal valid TS packet with the given header fields.
func buildPacket(pid uint16, pusi bool, afc uint8, cc uint8) []byte {
	b := make([]byte, PacketSize)
	b[0] = SyncByte
	if pusi {
		b[1] |= 0x40
	}
	b[1] |= byte(pid >> 8)
	b[2] = byte(pid)
	b[3] = afc<<4 | cc&0x0F
	for i := 5; i < PacketSize; i++ {
		b[i] = 0xFF
	}
	if afc == AFCAdaptationOnly {
		b[4] = 0 // adaptation_field_length = 0
	} else {
		b[4] = 0xFF
	}
	return b
}

func buildPCRPacket(pid uint16, cc uint8, base int64, ext int64) []byte {
	b := buildPacket(pid, false, AFCPayloadAndAF, cc)
	// adaptation_field_length=7, flags=PCR(0x10)|random(0x40)
	b[4] = 7
	b[5] = 0x50
	b[6] = byte(base >> 25)
	b[7] = byte(base >> 17)
	b[8] = byte(base >> 9)
	b[9] = byte(base >> 1)
	b[10] = byte(base&1)<<7 | byte(ext>>8)
	b[11] = byte(ext)
	return b
}

func TestParseValidPacketAndPCR(t *testing.T) {
	b := buildPCRPacket(0x0100, 3, 90_000, 150)
	ec := &mediaerr.Collector{}
	r := NewReader(bytes.NewReader(b), ec)
	p, err := r.ReadPacket()
	if err != nil {
		t.Fatal(err)
	}
	if p.PID != 0x0100 || p.CC != 3 || !p.HasPCR || !p.RandomAccess {
		t.Fatalf("unexpected packet: %+v", p)
	}
	if p.PCRBase != 90_000 || p.PCRExt != 150 {
		t.Errorf("PCR base=%d ext=%d", p.PCRBase, p.PCRExt)
	}
	if p.PCRRaw() != 90_000*300+150 {
		t.Errorf("PCRRaw=%d", p.PCRRaw())
	}
	if p.Offset != 0 {
		t.Errorf("offset=%d", p.Offset)
	}
	if len(ec.All()) != 0 {
		t.Errorf("unexpected errors: %v", ec.All())
	}
	if _, err := r.ReadPacket(); !errors.Is(err, io.EOF) {
		t.Errorf("want EOF, got %v", err)
	}
}

func TestContinuityGapRepeatJump(t *testing.T) {
	pkts := append(append(
		buildPacket(0x100, false, AFCPayloadOnly, 0),
		buildPacket(0x100, false, AFCPayloadOnly, 2)...), // gap (missing 1)
		buildPacket(0x100, false, AFCPayloadOnly, 2)..., // repeat
	)
	pkts = append(pkts, buildPacket(0x100, false, AFCPayloadOnly, 13)...) // jump

	ec := &mediaerr.Collector{}
	r := NewReader(bytes.NewReader(pkts), ec)
	for i := 0; i < 4; i++ {
		if _, err := r.ReadPacket(); err != nil {
			t.Fatal(err)
		}
	}
	if ec.CountByCode(mediaerr.CodeCCGap) != 1 {
		t.Error("missing CC gap event")
	}
	if ec.CountByCode(mediaerr.CodeCCRepeat) != 1 {
		t.Error("missing CC repeat event")
	}
	if ec.CountByCode(mediaerr.CodeCCJump) != 1 {
		t.Error("missing CC jump event")
	}
	for _, e := range ec.All() {
		if e.PID != 0x100 {
			t.Errorf("error PID=%x", e.PID)
		}
	}
}

func TestAdaptationOnlyDoesNotIncrementCC(t *testing.T) {
	pkts := append(
		buildPacket(0x200, false, AFCPayloadOnly, 5),
		buildPacket(0x200, false, AFCAdaptationOnly, 5)...,
	)
	pkts = append(pkts, buildPacket(0x200, false, AFCPayloadOnly, 6)...)
	ec := &mediaerr.Collector{}
	r := NewReader(bytes.NewReader(pkts), ec)
	for i := 0; i < 3; i++ {
		if _, err := r.ReadPacket(); err != nil {
			t.Fatal(err)
		}
	}
	if len(ec.All()) != 0 {
		t.Errorf("adaptation-only CC repeat must be legal, got %v", ec.All())
	}
}

func TestSyncLossAndResync(t *testing.T) {
	good := buildPacket(0x100, false, AFCPayloadOnly, 0)
	good2 := buildPacket(0x100, false, AFCPayloadOnly, 1)
	good3 := buildPacket(0x100, false, AFCPayloadOnly, 2) // confirms good2's sync
	garbage := make([]byte, PacketSize)
	for i := range garbage {
		garbage[i] = 0x00
	}
	stream := append(append(append(good, garbage...), good2...), good3...)
	ec := &mediaerr.Collector{}
	r := NewReader(bytes.NewReader(stream), ec)
	if _, err := r.ReadPacket(); err != nil {
		t.Fatal(err)
	}
	p, err := r.ReadPacket()
	if err != nil {
		t.Fatalf("resync failed: %v", err)
	}
	if p.Offset != int64(2*PacketSize) {
		t.Errorf("recovered offset=%d want %d", p.Offset, 2*PacketSize)
	}
	if ec.CountByCode(mediaerr.CodeSyncLost) == 0 {
		t.Error("sync loss not recorded")
	}
}

func TestTruncatedPacket(t *testing.T) {
	ec := &mediaerr.Collector{}
	r := NewReader(bytes.NewReader(make([]byte, 50)), ec)
	if _, err := r.ReadPacket(); err == nil {
		t.Fatal("expected error on truncated stream")
	}
}

func TestTruncatedTailAfterGoodPackets(t *testing.T) {
	// A full packet followed by a non-sync short tail must surface a
	// truncation error rather than silently skipping the tail.
	good := buildPacket(0x100, false, AFCPayloadOnly, 0)
	tail := make([]byte, 50)
	for i := range tail {
		tail[i] = 0xAB
	}
	ec := &mediaerr.Collector{}
	r := NewReader(bytes.NewReader(append(good, tail...)), ec)
	if _, err := r.ReadPacket(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadPacket(); err == nil {
		t.Fatal("expected truncation error on short non-sync tail")
	}
	if ec.CountByCode(mediaerr.CodeSyncLost) == 0 {
		t.Error("sync loss should be recorded before truncation")
	}
}

func TestAdaptationLengthOverflow(t *testing.T) {
	b := buildPacket(0x100, false, AFCPayloadAndAF, 0)
	b[4] = 200 // claims 200 adaptation bytes inside a 188-byte packet
	ec := &mediaerr.Collector{}
	r := NewReader(bytes.NewReader(b), ec)
	if _, err := r.ReadPacket(); err != nil {
		t.Fatal(err)
	}
	if ec.CountByCode(mediaerr.CodeAdaptationOverflow) != 1 {
		t.Errorf("errors: %v", ec.All())
	}
}
