package rtp

import (
	"encoding/binary"
	"testing"

	pionrtp "github.com/pion/rtp"

	"replaykit/media/fault"
)

// framePacket marshals one RTP packet and wraps it with the 2-byte
// big-endian length prefix used by the input format.
func framePacket(t *testing.T, seq uint16, ts uint32, payload []byte) []byte {
	t.Helper()
	p := pionrtp.Packet{
		Header:  pionrtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: 7},
		Payload: payload,
	}
	raw, err := p.Marshal()
	if err != nil {
		t.Fatalf("marshal rtp: %v", err)
	}
	out := make([]byte, 2+len(raw))
	binary.BigEndian.PutUint16(out, uint16(len(raw)))
	copy(out[2:], raw)
	return out
}

func parseOrFatal(t *testing.T, stream []byte) []Packet {
	t.Helper()
	pkts, anomalies, err := ParseFramed(stream, 2048)
	if err != nil {
		t.Fatalf("parse framed: %v", err)
	}
	for _, a := range anomalies {
		if a.Category == fault.CategoryPacketMalformed {
			t.Fatalf("unexpected malformed anomaly: %+v", a)
		}
	}
	return pkts
}

func TestExtendSequenceWraparound(t *testing.T) {
	cases := []struct {
		seq       uint16
		reference int64
		want      int64
	}{
		{0, 0, 0},
		{65535, 65534, 65535},
		{0, 65535, 65536},
		{1, 65536, 65537},
		{2, 65537, 65538},
	}
	for _, c := range cases {
		if got := ExtendSequence(c.seq, c.reference); got != c.want {
			t.Fatalf("ExtendSequence(%d,%d)=%d want %d", c.seq, c.reference, got, c.want)
		}
	}
}

func TestOrderDetectsOneLossAndOneDuplicate(t *testing.T) {
	// Arrival order 0,1,1(dup),2,4,5: sequence 3 is missing exactly once.
	seqs := []uint16{0, 1, 1, 2, 4, 5}
	var stream []byte
	for i, seq := range seqs {
		ts := uint32(seq) * 960
		stream = append(stream, framePacket(t, seq, ts, []byte{0x08, byte(i)})...)
	}
	pkts := parseOrFatal(t, stream)
	if len(pkts) != len(seqs) {
		t.Fatalf("parsed %d packets, want %d", len(pkts), len(seqs))
	}
	res := Order(pkts, DefaultOptions(48000))
	if len(res.Duplicates) != 1 {
		t.Fatalf("duplicates=%d want 1 (%+v)", len(res.Duplicates), res.Duplicates)
	}
	if len(res.Lost) != 1 {
		t.Fatalf("lost=%d want 1", len(res.Lost))
	}
	if res.Lost[0].Extended != 3 {
		t.Fatalf("lost extended seq=%d want 3", res.Lost[0].Extended)
	}
	// Six arrivals minus one duplicate = five unique packets; the missing
	// packet is never synthesised into the accepted media list.
	if len(res.Packets) != 5 {
		t.Fatalf("accepted packets=%d want 5", len(res.Packets))
	}
	for _, p := range res.Packets {
		if p.Extended == 3 {
			t.Fatal("missing sequence 3 must not be fabricated")
		}
	}
	cats := map[fault.Category]int{}
	for _, a := range res.Anomalies {
		cats[a.Category]++
	}
	if cats[fault.CategoryPacketLost] != 1 || cats[fault.CategoryPacketDuplicate] != 1 {
		t.Fatalf("anomaly categories=%v want one lost and one duplicate", cats)
	}
}

func TestOrderReordersWithinWindow(t *testing.T) {
	// seq 2 arrives before seq 1, but within the 80ms window both survive and
	// the output is media order.
	seqs := []uint16{0, 2, 1, 3}
	var stream []byte
	for _, seq := range seqs {
		stream = append(stream, framePacket(t, seq, uint32(seq)*960, []byte{0x08})...)
	}
	res := Order(parseOrFatal(t, stream), DefaultOptions(48000))
	if len(res.Packets) != 4 {
		t.Fatalf("packets=%d want 4", len(res.Packets))
	}
	for i, p := range res.Packets {
		if p.Extended != int64(i) {
			t.Fatalf("position %d extended=%d want %d", i, p.Extended, i)
		}
	}
	if len(res.Late) != 0 {
		t.Fatalf("late=%d want 0 for in-window reorder", len(res.Late))
	}
}

func TestWindowFromMSIs80ms(t *testing.T) {
	if DefaultReorderWindowMS != 80 {
		t.Fatalf("default window=%dms want 80", DefaultReorderWindowMS)
	}
	if got := WindowFromMS(80, 48000); got != 3840 {
		t.Fatalf("window ticks=%d want 3840", got)
	}
}

func TestParseFramedRejectsOverlongDeclaration(t *testing.T) {
	bad := []byte{0xff, 0xff, 0x00} // declares 65535 but has no payload
	pkts, anomalies, err := ParseFramed(bad, 2048)
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if len(pkts) != 0 {
		t.Fatalf("expected no packets, got %d", len(pkts))
	}
	if len(anomalies) != 1 {
		t.Fatalf("anomalies=%d want 1", len(anomalies))
	}
}
