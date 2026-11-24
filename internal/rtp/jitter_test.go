package rtp

import (
	"encoding/binary"
	"testing"

	"github.com/pion/rtp"

	"mediacheck/internal/clock"
)

func mkPacket(seq uint16, ts uint32, frame uint32, key bool) *rtp.Packet {
	payload := make([]byte, PayloadHeaderLen+8)
	binary.BigEndian.PutUint32(payload[0:4], frame)
	if key {
		payload[4] = FlagKeyframe
	}
	return &rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts},
		Payload: payload,
	}
}

func seqs(pkts []*rtp.Packet) []uint16 {
	out := make([]uint16, len(pkts))
	for i, p := range pkts {
		out[i] = p.SequenceNumber
	}
	return out
}

func TestJitterReorderAndDuplicate(t *testing.T) {
	b := NewBuffer(4)
	var out []*rtp.Packet
	push := func(p *rtp.Packet) { out = append(out, b.Push(p)...) }

	push(mkPacket(0, 0, 0, true))
	push(mkPacket(2, 0, 0, false))   // out of order
	push(mkPacket(1, 0, 0, false))   // fills gap, inside window
	push(mkPacket(2, 0, 0, false))   // duplicate
	push(mkPacket(3, 0, 0, false))
	out = append(out, b.Flush()...)

	want := []uint16{0, 1, 2, 3}
	got := seqs(out)
	if len(got) != len(want) {
		t.Fatalf("emitted seqs %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("emitted seqs %v, want %v", got, want)
		}
	}
	st := b.Stats()
	if st.Reordered != 1 || st.Duplicates != 1 || st.Lost != 0 {
		t.Fatalf("stats = %+v, want 1 reordered, 1 duplicate, 0 lost", st)
	}
}

func TestJitterLossOutsideWindow(t *testing.T) {
	b := NewBuffer(2)
	var out []*rtp.Packet
	push := func(p *rtp.Packet) { out = append(out, b.Push(p)...) }

	push(mkPacket(10, 9000, 1, false))
	// seq 11 never arrives; 12,13,14 force it outside the window.
	push(mkPacket(12, 9000, 1, false))
	push(mkPacket(13, 18000, 2, false))
	push(mkPacket(14, 18000, 2, false))
	// Late arrival of 11 after it was declared lost.
	push(mkPacket(11, 9000, 1, false))
	out = append(out, b.Flush()...)

	st := b.Stats()
	if st.Lost != 1 {
		t.Fatalf("lost = %d, want 1", st.Lost)
	}
	if st.Late != 1 {
		t.Fatalf("late = %d, want 1", st.Late)
	}
	if lost := b.Lost(); len(lost) != 1 || lost[0] != 11 {
		t.Fatalf("lost seqs = %v, want [11]", lost)
	}
	for _, p := range out {
		if p.SequenceNumber == 11 {
			t.Fatal("late packet must not be emitted after loss declaration")
		}
	}
}

func TestClassifyRecoverableAndUnrecoverable(t *testing.T) {
	ts := clock.Timescale(90000)
	// Frames of 2 packets each; frame 0 and frame 4 are keyframes.
	ordered := []*rtp.Packet{
		mkPacket(0, 0, 0, true), mkPacket(1, 0, 0, true),
		mkPacket(2, 3600, 1, false), mkPacket(3, 3600, 1, false),
		// gap: seq 4 lost (frame 2, non-key)
		mkPacket(5, 7200, 2, false),
		mkPacket(6, 10800, 3, false), mkPacket(7, 10800, 3, false),
		mkPacket(8, 14400, 4, true), mkPacket(9, 14400, 4, true),
	}
	lost := []uint16{4}
	ranges := Classify(ordered, lost, ts)
	if len(ranges) != 1 {
		t.Fatalf("ranges = %d, want 1", len(ranges))
	}
	r := ranges[0]
	if r.Recoverable {
		t.Fatal("gap before non-keyframe must be unrecoverable")
	}
	// Unrecoverable extent runs to the next keyframe at ts 14400.
	if r.EndNanos != ts.ToNanos(14400) {
		t.Fatalf("end = %d ns, want %d ns", r.EndNanos, ts.ToNanos(14400))
	}

	// Gap right before a keyframe is recoverable.
	lost2 := []uint16{7}
	ranges2 := Classify(ordered, lost2, ts)
	if len(ranges2) != 1 || !ranges2[0].Recoverable {
		t.Fatalf("gap before keyframe must be recoverable: %+v", ranges2)
	}
	if ranges2[0].EndNanos != ts.ToNanos(14400) {
		t.Fatalf("recoverable end = %d, want %d", ranges2[0].EndNanos, ts.ToNanos(14400))
	}
}

func TestClassifyNoLoss(t *testing.T) {
	if r := Classify([]*rtp.Packet{mkPacket(0, 0, 0, true)}, nil, 90000); r != nil {
		t.Fatalf("expected no ranges, got %+v", r)
	}
}
