package ingest

import (
	"encoding/binary"
	"testing"

	"github.com/pion/rtcp"

	"replay/internal/fault"
)

// buildSR serialises one RTCP sender report with the requested fields.
func buildSR(t *testing.T, ssrc uint32, ntp uint64, rtpTime, packetCount uint32) []byte {
	t.Helper()
	sr := &rtcp.SenderReport{
		SSRC:        ssrc,
		NTPTime:     ntp,
		RTPTime:     rtpTime,
		PacketCount: packetCount,
	}
	raw, err := sr.Marshal()
	if err != nil {
		t.Fatalf("marshal sr: %v", err)
	}
	return raw
}

func TestParseRTCPExtractsReport(t *testing.T) {
	ntp := encodeNTP(1_700_000_000, 0)
	raw := buildSR(t, 0x11223344, ntp, 96000, 42)
	report, err := ParseRTCPWithPion("audio", raw, 128, 0)
	if err != nil {
		t.Fatalf("ParseRTCP: %v", err)
	}
	if report.SSRC != 0x11223344 || report.RTPTime != 96000 || report.PacketCount != 42 {
		t.Fatalf("report=%+v", report)
	}
	if report.Offset != 128 {
		t.Fatalf("offset=%d", report.Offset)
	}
}

func TestParseRTCPTruncatedReportsExactOffset(t *testing.T) {
	raw := buildSR(t, 1, encodeNTP(100, 0), 48000, 4)
	cut := len(raw) - 6
	_, err := ParseRTCP("audio", raw[:cut], 64, 2)
	if err == nil {
		t.Fatal("expected truncation error")
	}
	fe, ok := fault.As(err)
	if !ok || fe.Kind != fault.KindTruncated {
		t.Fatalf("kind=%v", err)
	}
	if !fe.HasOffset || fe.ByteOffset != 64+int64(cut) {
		t.Fatalf("offset=%d want %d", fe.ByteOffset, 64+int64(cut))
	}
	if fe.Track != "audio" || !fe.HasIndex || fe.InputIndex != 2 {
		t.Fatalf("missing context: %s", fe.Error())
	}
}

func TestParseRTCPTooShortReportsRecordOffset(t *testing.T) {
	_, err := ParseRTCP("audio", []byte{0x80, 0xc8}, 9, 0)
	fe, ok := fault.As(err)
	if !ok || !fe.HasOffset || fe.ByteOffset != 9 {
		t.Fatalf("offset: %v", err)
	}
}

func TestParseRTCPRejectsWrongPacketType(t *testing.T) {
	raw := buildSR(t, 1, encodeNTP(1, 0), 1, 1)
	raw[1] = 201 // receiver report type
	_, err := ParseRTCP("audio", raw, 0, 0)
	if err == nil || !fault.IsKind(err, fault.KindInput) {
		t.Fatalf("want input error, got %v", err)
	}
}

func TestParseRTCPRejectsImplausibleLength(t *testing.T) {
	raw := buildSR(t, 1, encodeNTP(1, 0), 1, 1)
	binary.BigEndian.PutUint16(raw[2:4], 1) // declares 8 bytes total
	_, err := ParseRTCP("audio", raw, 0, 0)
	if err == nil || !fault.IsKind(err, fault.KindInput) {
		t.Fatalf("want input error, got %v", err)
	}
}

func encodeNTP(seconds uint32, fraction uint32) uint64 {
	return uint64(seconds)<<32 | uint64(fraction)
}
