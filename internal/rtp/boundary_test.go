package rtp

import (
	"testing"

	"github.com/pion/rtp"

	"mediapack/internal/fault"
)

func datagram(t *testing.T, seq uint16, payload []byte) []byte {
	t.Helper()
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			SSRC:           4242,
		},
		Payload: payload,
	}
	raw, err := pkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestContinuousSequenceAccepted(t *testing.T) {
	v := NewVerifier()
	for seq := uint16(100); seq < 105; seq++ {
		b, err := v.Check(datagram(t, seq, []byte{byte(seq)}), int64(seq)*10, 1)
		if err != nil {
			t.Fatalf("seq %d rejected: %v", seq, err)
		}
		if b.SequenceNumber != seq || b.MediaOffset != int64(seq)*10 {
			t.Fatalf("boundary mapping wrong: %+v", b)
		}
	}
}

func TestSequenceWrapAroundIsContinuity(t *testing.T) {
	v := NewVerifier()
	if _, err := v.Check(datagram(t, 65535, []byte("a")), 0, 1); err != nil {
		t.Fatal(err)
	}
	b, err := v.Check(datagram(t, 0, []byte("b")), 1, 1)
	if err != nil {
		t.Fatalf("wrap to 0 must be continuous: %v", err)
	}
	if b.SequenceNumber != 0 {
		t.Fatal("boundary sequence wrong after wrap")
	}
}

func TestDuplicateReportedWithOffset(t *testing.T) {
	v := NewVerifier()
	if _, err := v.Check(datagram(t, 5, []byte("x")), 50, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Check(datagram(t, 6, []byte("y")), 60, 1); err != nil {
		t.Fatal(err)
	}
	_, err := v.Check(datagram(t, 5, []byte("z")), 70, 1)
	fe := fault.Of(err)
	if fe == nil || fe.Kind != fault.KindRTPSequence || fe.Offset != 70 {
		t.Fatalf("expected duplicate fault at media offset 70, got %v", err)
	}
}

func TestJumpReportedWithSequenceAndOffset(t *testing.T) {
	v := NewVerifier()
	if _, err := v.Check(datagram(t, 10, []byte("a")), 100, 1); err != nil {
		t.Fatal(err)
	}
	_, err := v.Check(datagram(t, 13, []byte("b")), 130, 1)
	fe := fault.Of(err)
	if fe == nil || fe.Kind != fault.KindRTPSequence {
		t.Fatalf("expected jump fault, got %v", err)
	}
	if fe.Offset != 130 {
		t.Fatalf("jump fault must carry media offset 130, got %d", fe.Offset)
	}
}

func TestPayloadBoundaryMismatch(t *testing.T) {
	original := []byte("payload")
	good := datagram(t, 1, original)
	// Truncate the datagram: it now carries fewer bytes than the expected
	// block length, which a permissive parser would accept silently.
	truncated := good[:len(good)-2]
	v := NewVerifier()
	_, err := v.Check(truncated, 0, len(original))
	if !fault.Is(err, fault.KindRTPSequence) {
		t.Fatalf("expected boundary fault for truncated datagram, got %v", err)
	}

	// A well-formed datagram with the correct expected length passes.
	if _, err := v.Check(good, 0, len(original)); err != nil {
		t.Fatalf("intact datagram must validate: %v", err)
	}
}

func TestCheckStreamStopsAtFirstAnomaly(t *testing.T) {
	var pairs [][]byte
	var offsets []int64
	var wants []int
	for seq := uint16(0); seq < 4; seq++ {
		pairs = append(pairs, datagram(t, seq, []byte{byte(seq)}))
		offsets = append(offsets, int64(seq)*8)
		wants = append(wants, 1)
	}
	// Insert a jump: next expected 4, send 9.
	pairs = append(pairs, datagram(t, 9, []byte("jump")))
	offsets = append(offsets, 99)
	wants = append(wants, 4)
	got, err := CheckStream(pairs, offsets, wants)
	if !fault.Is(err, fault.KindRTPSequence) {
		t.Fatalf("expected stream fault, got %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("should have accepted 4 packets before fault, got %d", len(got))
	}
}

func TestPayloadNotMutatedByVerification(t *testing.T) {
	original := []byte("immutable-media-payload")
	dg := datagram(t, 1, append([]byte(nil), original...))
	v := NewVerifier()
	b, err := v.Check(dg, 0, len(original))
	if err != nil {
		t.Fatal(err)
	}
	// Re-parse and confirm the transported payload survives untouched.
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(dg); err != nil {
		t.Fatal(err)
	}
	if string(pkt.Payload) != string(original) {
		t.Fatal("verification changed the RTP payload")
	}
	if b.PayloadLen != len(original) {
		t.Fatalf("boundary payload len=%d want %d", b.PayloadLen, len(original))
	}
}
