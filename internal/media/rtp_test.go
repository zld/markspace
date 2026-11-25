package media

import (
	"testing"

	"github.com/pion/rtp"

	"archivecheck/internal/diagnostic"
)

func marshalTestPacket(t *testing.T, ts uint32) []byte {
	t.Helper()
	pkt := rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: 42,
			Timestamp:      ts,
			SSRC:           0x11223344,
		},
		Payload: []byte("sample"),
	}
	data, err := pkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseRTPValidTimestamp(t *testing.T) {
	data := marshalTestPacket(t, 90000)
	parsed, err := ParseRTP(8, 32, data, 90000, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.TimeDeltaNS != 0 || parsed.TimeNS != 1_000_000_000 {
		t.Fatalf("time = %d delta=%d", parsed.TimeNS, parsed.TimeDeltaNS)
	}
	if parsed.Packet.SequenceNumber != 42 || parsed.SequenceKey() == "" {
		t.Fatalf("packet not parsed: %+v", parsed.Packet)
	}
}

func TestParseRTPBadTimestampReportsFieldOffset(t *testing.T) {
	data := marshalTestPacket(t, 80000)
	_, err := ParseRTP(9, 100, data, 90000, 90000)
	if !diagnostic.HasCode(err, diagnostic.CodeBadRTPTimestamp) {
		t.Fatalf("err = %v, want BAD_RTP_TIMESTAMP", err)
	}
	de, _ := diagnostic.From(err)
	if de.Sample != 9 || de.Offset != 104 {
		t.Fatalf("location = sample%d offset%d, want 9/104", de.Sample, de.Offset)
	}
}

func TestParseRTPMalformedPacket(t *testing.T) {
	_, err := ParseRTP(10, 0, []byte{0x80, 0x60}, 90000, 0)
	if !diagnostic.HasCode(err, diagnostic.CodeInvalidRTP) {
		t.Fatalf("err = %v, want INVALID_RTP", err)
	}
}

func TestWrapAwareDelta(t *testing.T) {
	if d := WrapAwareDelta(0xfffffff0, 0x10); d != 32 {
		t.Fatalf("wrap delta = %d, want 32", d)
	}
}
