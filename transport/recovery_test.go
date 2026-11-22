package transport

import (
	"testing"

	"github.com/pion/rtp"

	"mediadist/timeline"
)

func packet(ssrc uint32, seq uint16, ts uint32, payload byte) *rtp.Packet {
	return &rtp.Packet{
		Header:  rtp.Header{SSRC: ssrc, SequenceNumber: seq, Timestamp: ts, PayloadType: 96, Version: 2},
		Payload: []byte{payload},
	}
}

func marshal(t *testing.T, p *rtp.Packet) []byte {
	t.Helper()
	data, err := p.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func TestRecoveryDuplicatesAndReorder(t *testing.T) {
	jb, err := NewJitterBuffer(Stream{SSRC: 42, ClockRate: 90000}, 8)
	if err != nil {
		t.Fatalf("NewJitterBuffer: %v", err)
	}
	var emitted []*rtp.Packet
	push := func(seq uint16, payload byte) PushResult {
		t.Helper()
		res, perr := jb.Push(packet(42, seq, uint32(seq)*3000, payload))
		if perr != nil {
			t.Fatalf("push seq %d: %v", seq, perr)
		}
		emitted = append(emitted, res.Output...)
		return res
	}
	// Arrival order: 0,1, 3 (hole at 2), 2 (late fill), 1 (dup).
	push(0, 0)
	push(1, 1)
	if push(3, 3).Duplicate {
		t.Fatal("seq 3 misclassified as duplicate")
	}
	if jb.Metrics().Missing != 1 {
		t.Fatalf("missing = %d, want 1", jb.Metrics().Missing)
	}
	push(2, 2)
	m := jb.Metrics()
	if m.Recovered != 1 || m.RecoveryRate() != 1.0 {
		t.Fatalf("recovery = %d rate %.2f, want 1 / 1.0", m.Recovered, m.RecoveryRate())
	}
	res, err := jb.Push(packet(42, 1, 3000, 1))
	if err != nil {
		t.Fatalf("duplicate push: %v", err)
	}
	if !res.Duplicate || jb.Metrics().Duplicates != 1 {
		t.Fatalf("duplicate handling wrong: %+v", jb.Metrics())
	}

	tail, missing := jb.Flush()
	emitted = append(emitted, tail...)
	if len(missing) != 0 || len(emitted) != 4 {
		t.Fatalf("flush: %d packets %d missing", len(emitted), len(missing))
	}
	for i, p := range emitted {
		if p.SequenceNumber != uint16(i) {
			t.Fatalf("output not ordered at %d: %d", i, p.SequenceNumber)
		}
	}
}

func TestRecoverySequenceWrap(t *testing.T) {
	jb, _ := NewJitterBuffer(Stream{SSRC: 7, ClockRate: 8000}, 4)
	r1, _ := jb.Push(packet(7, 65534, 100, 1))
	r2, _ := jb.Push(packet(7, 65535, 200, 2))
	r3, _ := jb.Push(packet(7, 0, 300, 3))
	if jb.Metrics().Wrapped != 1 {
		t.Fatalf("wrap count = %d, want 1", jb.Metrics().Wrapped)
	}
	out, missing := jb.Flush()
	out = append(append(append(out, r1.Output...), r2.Output...), r3.Output...)
	if len(missing) != 0 || len(out) != 3 {
		t.Fatalf("wrap flush: %d packets %d missing", len(out), len(missing))
	}
	if out[2].SequenceNumber != 0 {
		t.Fatalf("post-wrap packet out of order: %d", out[2].SequenceNumber)
	}
	if got := SeqDistance(65535, 0); got != 1 {
		t.Fatalf("SeqDistance wrap = %d, want 1", got)
	}
}

func TestRecoveryUnrecoverableWindow(t *testing.T) {
	jb, _ := NewJitterBuffer(Stream{SSRC: 9, ClockRate: 90000}, 2)
	jb.Push(packet(9, 0, 0, 0))
	jb.Push(packet(9, 1, 1, 1))
	// Sequence 2 never arrives; jump far enough to age the hole past window 2.
	jb.Push(packet(9, 5, 5, 5))
	m := jb.Metrics()
	if m.Missing != 3 || m.Unrecoverable < 1 {
		t.Fatalf("after jump missing=%d unrecoverable=%d, want 3 / >=1",
			m.Missing, m.Unrecoverable)
	}

	// Late packet for a dead hole must be rejected, never re-inserted.
	_, err := jb.Push(packet(9, 2, 2, 2))
	if timeline.CodeOf(err) != "RTP_UNRECOVERABLE_LATE" {
		t.Fatalf("late dead-hole code = %q", timeline.CodeOf(err))
	}
	out, _ := jb.Flush()
	for _, p := range out {
		if p.SequenceNumber == 2 {
			t.Fatal("unrecoverable sequence was fabricated or reinserted")
		}
	}
	// Recovered packets must never exceed distinct missing packets.
	if jb.Metrics().Recovered > jb.Metrics().Missing {
		t.Fatal("recovered exceeds missing")
	}
}

func TestParsePacketRejectsMalformed(t *testing.T) {
	if _, err := ParsePacket(nil); timeline.CodeOf(err) != "RTP_MALFORMED" {
		t.Fatalf("empty code = %q", timeline.CodeOf(err))
	}
	big := make([]byte, MaxPacketBytes+1)
	if _, err := ParsePacket(big); timeline.CodeOf(err) != "RTP_MALFORMED" {
		t.Fatalf("oversize code = %q", timeline.CodeOf(err))
	}
	good := marshal(t, packet(1, 0, 0, 0))
	if _, err := ParsePacket(good); err != nil {
		t.Fatalf("valid packet rejected: %v", err)
	}
}

func TestStreamEventRationalTime(t *testing.T) {
	s := Stream{SSRC: 1, ClockRate: 90000}
	ev, err := s.Event(packet(1, 0, 90000, 0), 0, 0)
	if err != nil {
		t.Fatalf("Event: %v", err)
	}
	ms, err := ev.Time.ScaleTo(1)
	if err != nil {
		t.Fatalf("scale: %v", err)
	}
	if ms != 1000 {
		t.Fatalf("event time = %d ms, want 1000", ms)
	}
	if _, err := s.Event(packet(2, 0, 0, 0), 0, 0); timeline.CodeOf(err) != "RTP_SSRC_MISMATCH" {
		t.Fatalf("ssrc mismatch code = %q", timeline.CodeOf(err))
	}
}
