package rtpcheck_test

import (
	"testing"

	"github.com/pion/rtp"

	"assetcheck/internal/rtpcheck"
)

func marshalPacket(t *testing.T, seq uint16, ts uint32, ssrc uint32) []byte {
	t.Helper()
	p := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 96, SequenceNumber: seq,
			Timestamp: ts, SSRC: ssrc,
		},
		Payload: []byte{0x00, 0x01, 0x02, 0x03},
	}
	b, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestValidateHealthyStream(t *testing.T) {
	var pkts [][]byte
	for i := 0; i < 5; i++ {
		pkts = append(pkts, marshalPacket(t, uint16(100+i), uint32(1000+i*960), 7))
	}
	rep, err := rtpcheck.Validate(pkts)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() || rep.PacketCount != 5 {
		t.Fatalf("healthy report wrong: %+v", rep)
	}
	if d := rtpcheck.ForwardDelta(1000, 1960); d != 960 {
		t.Fatalf("delta=%d", d)
	}
	if ns := rtpcheck.TimestampToNanos(48000, 48000); ns != 1_000_000_000 {
		t.Fatalf("48000 ticks @48k = %d ns, want 1e9", ns)
	}
}

func TestValidateGapDuplicateAndBackwardTimestamp(t *testing.T) {
	pkts := [][]byte{
		marshalPacket(t, 10, 1000, 9),
		marshalPacket(t, 11, 1960, 9),
		marshalPacket(t, 11, 1960, 9), // duplicate sequence
		marshalPacket(t, 14, 2920, 9), // gap: missing 12,13
		marshalPacket(t, 15, 2000, 9), // backward timestamp
	}
	rep, err := rtpcheck.Validate(pkts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.SequenceGaps != 1 || rep.MissingPackets != 2 {
		t.Fatalf("gaps=%d missing=%d", rep.SequenceGaps, rep.MissingPackets)
	}
	if rep.DuplicateSequence != 1 {
		t.Fatalf("duplicates=%d", rep.DuplicateSequence)
	}
	if rep.TimestampBackward != 1 {
		t.Fatalf("backward=%d", rep.TimestampBackward)
	}
	if rep.OK() {
		t.Fatal("report must not be OK")
	}
}

func TestValidateRejectsMalformedPacket(t *testing.T) {
	if _, err := rtpcheck.ParsePacket([]byte{0x80, 0x60}); err == nil {
		t.Fatal("expected error for truncated RTP packet")
	}
	if _, err := rtpcheck.Validate([][]byte{{0xff, 0xff}}); err == nil {
		t.Fatal("expected no parseable packets error")
	}
}

func TestSequenceWrapForwardDelta(t *testing.T) {
	// 65535 -> 0 must be treated as contiguous by the validator.
	pkts := [][]byte{
		marshalPacket(t, 65535, 0xfffffff0, 3),
		marshalPacket(t, 0, 480, 3),
	}
	rep, err := rtpcheck.Validate(pkts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.SequenceGaps != 0 {
		t.Fatalf("wrap should be contiguous, gaps=%d issues=%+v", rep.SequenceGaps, rep.Issues)
	}
}
