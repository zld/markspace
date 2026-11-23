package rtpparse

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/pion/rtp"

	"mediarun/internal/fault"
)

// datagram builds one encoded RTP packet.
func datagram(seq uint16, ts uint32, pt uint8, ssrc uint32, payload []byte) []byte {
	p := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           ssrc,
			PayloadType:    pt,
			Marker:         true,
		},
		Payload: payload,
	}
	raw, err := p.Marshal()
	if err != nil {
		panic(err)
	}
	return raw
}

// sidecar frames datagrams with the length-prefixed layout the parser expects.
func sidecar(pkts ...[]byte) []byte {
	var buf bytes.Buffer
	buf.WriteString(Magic)
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(pkts)))
	buf.Write(n[:])
	for _, p := range pkts {
		binary.BigEndian.PutUint32(n[:], uint32(len(p)))
		buf.Write(n[:])
		buf.Write(p)
	}
	return buf.Bytes()
}

func TestParseSidecarRoundTrip(t *testing.T) {
	a := datagram(1, 960, 111, 0xAABBCCDD, []byte("opus-frame-a"))
	b := datagram(2, 1920, 111, 0xAABBCCDD, []byte("opus-frame-b"))
	data := sidecar(a, b)
	pkts, faults := ParseSidecar(bytes.NewReader(data), int64(len(data)))
	if faults.Len() != 0 {
		t.Fatalf("faults: %v", faults)
	}
	if len(pkts) != 2 {
		t.Fatalf("packets = %d", len(pkts))
	}
	if pkts[0].Sequence != 1 || pkts[1].Timestamp != 1920 {
		t.Fatalf("header mismatch: %+v", pkts[0].Header)
	}
	if string(pkts[1].Payload) != "opus-frame-b" {
		t.Fatalf("payload = %q", pkts[1].Payload)
	}
	if pkts[1].Offset != int64(8+4+len(a)) {
		t.Fatalf("second offset = %d", pkts[1].Offset)
	}
}

func TestParseSidecarTruncatedDatagram(t *testing.T) {
	a := datagram(1, 960, 111, 1, bytes.Repeat([]byte{1}, 32))
	b := datagram(2, 1920, 111, 1, bytes.Repeat([]byte{2}, 32))
	data := sidecar(a, b)
	// Truncate the last datagram in the middle of its payload.
	data = data[:len(data)-10]
	_, faults := ParseSidecar(bytes.NewReader(data), int64(len(data)))
	if faults.CountKind(fault.KindTruncated) != 1 {
		t.Fatalf("faults = %s", faults.Summary())
	}
	if faults[0].Offset <= 0 {
		t.Fatalf("offset = %d", faults[0].Offset)
	}
}

func TestParseSidecarRejectsBadMagicAndHugeCount(t *testing.T) {
	bad := []byte("XXXX\x00\x00\x00\x01")
	_, faults := ParseSidecar(bytes.NewReader(bad), int64(len(bad)))
	if faults.CountKind(fault.KindVersion) != 1 {
		t.Fatalf("faults = %s", faults.Summary())
	}
	var huge bytes.Buffer
	huge.WriteString(Magic)
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], MaxPackets+1)
	huge.Write(n[:])
	_, faults = ParseSidecar(bytes.NewReader(huge.Bytes()), int64(huge.Len()))
	if faults.CountKind(fault.KindLimit) != 1 {
		t.Fatalf("faults = %s", faults.Summary())
	}
}

func TestParsePacketRejectsMalformed(t *testing.T) {
	_, err := ParsePacket([]byte{0x80}, 42, 3)
	if err == nil {
		t.Fatal("short datagram accepted")
	}
	fe, ok := fault.As(err)
	if !ok || fe.Offset != 42 || fe.Record != 3 {
		t.Fatalf("fault = %v", err)
	}
}

func TestValidateProfile(t *testing.T) {
	pkt, err := ParsePacket(datagram(5, 0, 96, 0x11, []byte("x")), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateProfile(pkt, 96, 0x11); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
	if err := ValidateProfile(pkt, 111, 0x11); err == nil {
		t.Fatal("wrong payload type accepted")
	} else if fe, _ := fault.As(err); !fe.Recoverable {
		t.Fatal("profile fault should be recoverable")
	}
	if err := ValidateProfile(pkt, 96, 0x22); err == nil {
		t.Fatal("wrong ssrc accepted")
	}
}

func TestSequenceWindowWrapForwardAndBackward(t *testing.T) {
	var w SequenceWindow
	if got := w.Extend(65534); got != 65534 {
		t.Fatalf("first = %d", got)
	}
	if got := w.Extend(65535); got != 65535 {
		t.Fatalf("second = %d", got)
	}
	if got := w.Extend(0); got != 65536 {
		t.Fatalf("forward wrap = %d want 65536", got)
	}
	if got := w.Extend(1); got != 65537 {
		t.Fatalf("after wrap = %d", got)
	}
	// A packet from just before the wrap resolves onto the previous cycle.
	if got := w.Extend(65535); got != 65535 {
		t.Fatalf("late before wrap = %d", got)
	}
}

func TestSequenceWindowReordering(t *testing.T) {
	var w SequenceWindow
	for _, seq := range []uint16{100, 102, 101, 104, 103} {
		w.Extend(seq)
	}
	if got := w.Extend(105); got != 105 {
		t.Fatalf("extended = %d", got)
	}
	if w.Highest() != 105 {
		t.Fatalf("highest = %d", w.Highest())
	}
}
