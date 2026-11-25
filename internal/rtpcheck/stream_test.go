package rtpcheck

import (
	"testing"

	"github.com/pion/rtp"

	"mediascan/internal/errs"
)

func framePacket(t *testing.T, seq uint16, ts uint32, payloadLen int, marker bool) []byte {
	t.Helper()
	p := rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           4242,
			Marker:         marker,
		},
		Payload: make([]byte, payloadLen),
	}
	for i := range p.Payload {
		p.Payload[i] = 0xAB
	}
	b, err := EncodeDatagram(&p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseStreamAndWrap(t *testing.T) {
	var data []byte
	// 3 packets at 90 kHz, the second crosses the 2^32 wrap boundary.
	data = append(data, framePacket(t, 0, 0xFFFFFFF0, 10, false)...)
	data = append(data, framePacket(t, 1, 0x00000005, 10, false)...)
	data = append(data, framePacket(t, 2, 0x0000000F, 10, true)...)

	s, err := ParseStream("track.rtp", data, 90000)
	if err != nil {
		t.Fatalf("ParseStream failed: %v", err)
	}
	if len(s.Packets) != 3 || s.SSRC != 4242 {
		t.Fatalf("stream = %+v", s)
	}
	want := []uint64{0xFFFFFFF0, 0x100000005, 0x10000000F}
	for i, w := range want {
		if s.Packets[i].TimestampExt != w {
			t.Fatalf("pkt %d ext = %#x, want %#x", i, s.Packets[i].TimestampExt, w)
		}
	}
	// Second datagram starts after the first framed packet (prefix+data)
	// and its own 2-byte prefix.
	wantOffset := int64(len(framePacket(t, 0, 0, 10, false))) + 2
	if s.Packets[1].ByteOffset != wantOffset {
		t.Fatalf("second packet offset = %d, want %d", s.Packets[1].ByteOffset, wantOffset)
	}
	if err := s.MarshalRoundTrip("track.rtp"); err != nil {
		t.Fatalf("round trip failed: %v", err)
	}
}

func TestSequenceGapRejected(t *testing.T) {
	var data []byte
	data = append(data, framePacket(t, 0, 100, 5, false)...)
	data = append(data, framePacket(t, 2, 200, 5, false)...) // skips 1
	_, err := ParseStream("gap.rtp", data, 90000)
	se, ok := errs.AsError(err)
	if !ok || se.Kind != errs.KindRTP || se.Offset == errs.Unknown {
		t.Fatalf("want located RTP error, got %v", err)
	}
}

func TestTruncatedPacket(t *testing.T) {
	good := framePacket(t, 0, 100, 8, false)
	// A 2-byte prefix promising 300 bytes that never arrive.
	bad := append(good, 0x01, 0x2C)
	_, err := ParseStream("trunc.rtp", bad, 90000)
	se, _ := errs.AsError(err)
	if se == nil || se.Kind != errs.KindRTP || se.Segment != 1 {
		t.Fatalf("want RTP truncation at segment 1, got %v", err)
	}
}

func TestTruncatedPrefix(t *testing.T) {
	good := framePacket(t, 0, 100, 8, false)
	// One stray byte where a length prefix should be.
	_, err := ParseStream("prefix.rtp", append(good, 0x7F), 90000)
	se, _ := errs.AsError(err)
	if se == nil || se.Kind != errs.KindRTP {
		t.Fatalf("want prefix truncation error, got %v", err)
	}
}

func TestBadVersion(t *testing.T) {
	bad := framePacket(t, 0, 100, 4, false)
	bad[2] = 0x40 // version bits -> 1 (datagram starts after 2-byte prefix)
	_, err := ParseStream("ver.rtp", bad, 90000)
	se, _ := errs.AsError(err)
	if se == nil || se.Kind != errs.KindRTP {
		t.Fatalf("want version error, got %v", err)
	}
}

func TestBoundaryWithinTolerance(t *testing.T) {
	var data []byte
	data = append(data, framePacket(t, 0, 0, 4, false)...)
	data = append(data, framePacket(t, 1, 9000, 4, true)...)
	s, err := ParseStream("b.rtp", data, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyBoundary("b.rtp", 9000, 20); err != nil {
		t.Fatalf("equal boundary should pass: %v", err)
	}
	// 2700 ticks @90k = 30 ms deviation, beyond the 20 ms window.
	if err := s.VerifyBoundary("b.rtp", 9000+2700, 20); err == nil {
		t.Fatal("30 ms deviation must exceed the 20 ms tolerance")
	}
}

func TestEmptyAndBadClockRate(t *testing.T) {
	if _, err := ParseStream("empty.rtp", nil, 90000); err == nil {
		t.Fatal("empty input must error")
	}
	if _, err := ParseStream("rate.rtp", framePacket(t, 0, 1, 1, false), 0); err == nil {
		t.Fatal("zero clock rate must error")
	}
}

func TestPaddingFraming(t *testing.T) {
	p := rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 96, SequenceNumber: 0,
			Timestamp: 1000, SSRC: 7, Padding: true,
		},
		Payload:     []byte{1, 2, 3},
		PaddingSize: 4,
	}
	framed, err := EncodeDatagram(&p)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseStream("pad.rtp", framed, 90000)
	if err != nil {
		t.Fatalf("padded packet parse failed: %v", err)
	}
	if s.Packets[0].PayloadLen != 3 {
		t.Fatalf("payload len = %d, want 3 (padding excluded)", s.Packets[0].PayloadLen)
	}
	if err := s.MarshalRoundTrip("pad.rtp"); err != nil {
		t.Fatalf("padded round trip: %v", err)
	}
}
