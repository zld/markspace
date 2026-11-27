package rtcp

import (
	"encoding/binary"
	"errors"
	"testing"

	"rtp-rebuild/internal/fault"
)

func testBlock(ssrc uint32, lost int32, jitter uint32) ReportBlock {
	return ReportBlock{
		SSRC: ssrc, FractionLost: 4, CumulativeLost: lost, HighestSeq: 5000,
		Jitter: jitter, LastSR: 12, DelaySinceSR: 34,
	}
}

func TestParseSenderReport(t *testing.T) {
	blocks := []ReportBlock{testBlock(0xAA, 7, 300), testBlock(0xBB, -2, 900)}
	raw := SenderReport(0x1234, 99000, 0x1122334455667788, 1000, 20000, blocks)
	reports, err := ParseCompound(raw, 4096)
	if err != nil {
		t.Fatalf("ParseCompound: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}
	rep := reports[0]
	if rep.Type != TypeSR || rep.SSRC != 0x1234 || rep.ReportCount != 2 {
		t.Fatalf("report = %+v", rep)
	}
	if rep.Offset != 4096 {
		t.Fatalf("offset = %d, want 4096", rep.Offset)
	}
	if rep.NTP != 0x1122334455667788 || rep.RTPTS != 99000 {
		t.Fatalf("sr timestamps = %#x %d", rep.NTP, rep.RTPTS)
	}
	if rep.PacketCount != 1000 || rep.OctetCount != 20000 {
		t.Fatalf("counts = %d %d", rep.PacketCount, rep.OctetCount)
	}
	if len(rep.Blocks) != 2 {
		t.Fatalf("blocks = %d", len(rep.Blocks))
	}
	if rep.Blocks[0].CumulativeLost != 7 || rep.Blocks[1].CumulativeLost != -2 {
		t.Fatalf("lost = %d %d", rep.Blocks[0].CumulativeLost, rep.Blocks[1].CumulativeLost)
	}
	if rep.Blocks[1].Offset != 4096+int64(headerLen+24+blockLen) {
		t.Fatalf("block offset = %d", rep.Blocks[1].Offset)
	}
	if rep.Blocks[0].Jitter != 300 || rep.Blocks[1].Jitter != 900 {
		t.Fatalf("jitter = %d %d", rep.Blocks[0].Jitter, rep.Blocks[1].Jitter)
	}
}

func TestParseReceiverReport(t *testing.T) {
	raw := ReceiverReport(0x42, []ReportBlock{testBlock(0x77, 3, 42)})
	reports, err := ParseCompound(raw, 0)
	if err != nil {
		t.Fatalf("ParseCompound: %v", err)
	}
	if len(reports) != 1 || reports[0].Type != TypeRR {
		t.Fatalf("reports = %+v", reports)
	}
	if reports[0].SSRC != 0x42 || len(reports[0].Blocks) != 1 {
		t.Fatalf("report = %+v", reports[0])
	}
}

func TestTruncatedReportBlock(t *testing.T) {
	raw := ReceiverReport(0x42, []ReportBlock{testBlock(0x77, 3, 42), testBlock(0x78, 4, 43)})
	// Declare two blocks but cut the payload so the second block is incomplete.
	cut := raw[:len(raw)-8]
	_, err := ParseCompound(cut, 512)
	if err == nil {
		t.Fatal("expected truncation error")
	}
	if !errors.Is(err, fault.ErrTruncated) {
		t.Fatalf("error = %v, want truncated", err)
	}
	stage, ok := fault.StageOf(err)
	if !ok || stage != fault.StageRTCP {
		t.Fatalf("stage = %v, %v", stage, ok)
	}
	off, ok := fault.OffsetOf(err)
	if !ok {
		t.Fatal("missing offset")
	}
	// 512 base + 4 header + 4 ssrc + 2 full blocks = the third would start at
	// 512+4+4+48 = 568; the error points at the second, which is short.
	if off < 512 {
		t.Fatalf("offset = %d should be inside the input", off)
	}
}

func TestTruncatedHeader(t *testing.T) {
	_, err := ParseCompound([]byte{0x80, 0xC8}, 100)
	if !errors.Is(err, fault.ErrTruncated) {
		t.Fatalf("error = %v", err)
	}
	if off, _ := fault.OffsetOf(err); off != 100 {
		t.Fatalf("offset = %d, want 100", off)
	}
}

func TestDeclaredLengthExceedsInput(t *testing.T) {
	raw := ReceiverReport(0x42, nil)
	binary.BigEndian.PutUint16(raw[2:4], 99) // claim a much larger packet
	_, err := ParseCompound(raw, 0)
	if !errors.Is(err, fault.ErrTruncated) {
		t.Fatalf("error = %v, want truncated", err)
	}
}

func TestBadVersion(t *testing.T) {
	raw := ReceiverReport(0x42, nil)
	raw[0] = 0x40 // version 1
	_, err := ParseCompound(raw, 0)
	if !errors.Is(err, fault.ErrCorrupt) {
		t.Fatalf("error = %v, want corrupt", err)
	}
}

func TestUnsupportedPacketType(t *testing.T) {
	raw := ReceiverReport(0x42, nil)
	raw[1] = 250
	_, err := ParseCompound(raw, 0)
	if !errors.Is(err, fault.ErrUnsupported) {
		t.Fatalf("error = %v, want unsupported", err)
	}
}

func TestSkipsSDES(t *testing.T) {
	sdes := make([]byte, 8)
	sdes[0] = 0x81
	sdes[1] = TypeSDES
	binary.BigEndian.PutUint16(sdes[2:4], 1)
	raw := append(sdes, ReceiverReport(0x42, []ReportBlock{testBlock(1, 1, 10)})...)
	reports, err := ParseCompound(raw, 0)
	if err != nil {
		t.Fatalf("ParseCompound: %v", err)
	}
	if len(reports) != 1 || reports[0].Type != TypeRR {
		t.Fatalf("reports = %+v", reports)
	}
}

func TestSummarize(t *testing.T) {
	raw := append(
		SenderReport(1, 2, 3, 4, 5, []ReportBlock{testBlock(9, 10, 100)}),
		ReceiverReport(2, []ReportBlock{testBlock(10, 4, 300)})...,
	)
	reports, err := ParseCompound(raw, 0)
	if err != nil {
		t.Fatalf("ParseCompound: %v", err)
	}
	sum := Summarize(reports)
	if sum.Blocks != 2 {
		t.Fatalf("blocks = %d", sum.Blocks)
	}
	if sum.LostTotal != 14 {
		t.Fatalf("lost total = %d, want 14", sum.LostTotal)
	}
	if sum.MaxJitter != 300 {
		t.Fatalf("max jitter = %d", sum.MaxJitter)
	}
	if sum.Jitter() != 200 {
		t.Fatalf("mean jitter = %v, want 200", sum.Jitter())
	}
	if sum.HighestSeq != 5000 {
		t.Fatalf("highest seq = %d", sum.HighestSeq)
	}
	empty := Summarize(nil)
	if empty.Jitter() != 0 || empty.Blocks != 0 {
		t.Fatalf("empty summary = %+v", empty)
	}
}

func TestNegativeCumulativeLoss(t *testing.T) {
	raw := ReceiverReport(1, []ReportBlock{{SSRC: 2, CumulativeLost: -5}})
	reports, err := ParseCompound(raw, 0)
	if err != nil {
		t.Fatalf("ParseCompound: %v", err)
	}
	if got := reports[0].Blocks[0].CumulativeLost; got != -5 {
		t.Fatalf("cumulative lost = %d, want -5", got)
	}
}

func TestZeroLengthCompound(t *testing.T) {
	reports, err := ParseCompound(nil, 0)
	if err != nil {
		t.Fatalf("ParseCompound: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("reports = %d", len(reports))
	}
}
