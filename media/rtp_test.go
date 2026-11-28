package media

import (
	"testing"

	"github.com/pion/rtp"

	"mediad/timeline"
)

func rtpPkt(seq uint16, ts uint32, marker bool, payload string) *rtp.Packet {
	return &rtp.Packet{
		Header:  rtp.Header{Version: 2, SSRC: 0x11223344, SequenceNumber: seq, Timestamp: ts, Marker: marker},
		Payload: []byte(payload),
	}
}

func TestRecoverRTPPerfectStream(t *testing.T) {
	var pkts []RTPPacket
	for i := 0; i < 10; i++ {
		pkts = append(pkts, RTPPacket{Packet: rtpPkt(uint16(i+1), uint32(i*3000), false, "x"), Arrival: i})
	}
	rec := RecoverRTP(pkts)
	if rec.Received != 10 || rec.Duplicates != 0 || rec.Unrecoverable != 0 || rec.Expected != 10 {
		t.Fatalf("rec = %+v", rec)
	}
	if rec.LossRecoveryRate() != 1 {
		t.Fatalf("loss rate = %v", rec.LossRecoveryRate())
	}
	for i, pe := range rec.Packets {
		if pe.Seq != int64(i+1) {
			t.Fatalf("packet %d seq %d", i, pe.Seq)
		}
	}
}

func TestRecoverRTPDuplicatesCountedOnce(t *testing.T) {
	pkts := []RTPPacket{
		{Packet: rtpPkt(1, 0, false, "a"), Arrival: 0},
		{Packet: rtpPkt(2, 3000, false, "b"), Arrival: 1},
		{Packet: rtpPkt(2, 3000, false, "b"), Arrival: 2}, // duplicate
		{Packet: rtpPkt(3, 6000, false, "c"), Arrival: 3},
	}
	rec := RecoverRTP(pkts)
	if rec.Received != 3 || rec.Duplicates != 1 || rec.Expected != 3 {
		t.Fatalf("rec = %+v", rec)
	}
	if len(rec.Packets) != 3 {
		t.Fatalf("packets = %d, want 3", len(rec.Packets))
	}
}

func TestRecoverRTPReordersBySequence(t *testing.T) {
	// Arrival order 1,3,2,4; recovery must present 1,2,3,4.
	pkts := []RTPPacket{
		{Packet: rtpPkt(1, 0, false, "a"), Arrival: 0},
		{Packet: rtpPkt(3, 6000, false, "c"), Arrival: 1},
		{Packet: rtpPkt(2, 3000, false, "b"), Arrival: 2},
		{Packet: rtpPkt(4, 9000, false, "d"), Arrival: 3},
	}
	rec := RecoverRTP(pkts)
	if len(rec.Packets) != 4 {
		t.Fatalf("packets = %d", len(rec.Packets))
	}
	for want, pe := range rec.Packets {
		if pe.Seq != int64(want+1) {
			t.Fatalf("position %d has seq %d", want, pe.Seq)
		}
	}
	if rec.Unrecoverable != 0 {
		t.Fatalf("reordered packets are not loss: %+v", rec)
	}
	if rec.Reordered < 1 {
		t.Fatalf("reordered count = %d", rec.Reordered)
	}
}

func TestRecoverRTPUnrecoverableBeyondWindow(t *testing.T) {
	// Sequence 5 is never delivered and the stream runs well past the window.
	var pkts []RTPPacket
	for i := 1; i <= 200; i++ {
		if i == 5 {
			continue
		}
		pkts = append(pkts, RTPPacket{Packet: rtpPkt(uint16(i), uint32(i*100), false, "x"), Arrival: i})
	}
	rec := RecoverRTP(pkts)
	if rec.Unrecoverable != 1 {
		t.Fatalf("unrecoverable = %d, want 1", rec.Unrecoverable)
	}
	if rec.Expected != 200 || rec.Received != 199 {
		t.Fatalf("expected=%d received=%d", rec.Expected, rec.Received)
	}
	if rate := rec.LossRecoveryRate(); rate < 0.99 || rate >= 1 {
		t.Fatalf("loss rate = %v", rate)
	}
	if !rec.Issues.HasErrors() {
		t.Fatal("a real gap must be recorded as an issue")
	}
}

func TestRecoverRTPLatePacketInsideWindowRecovered(t *testing.T) {
	// Sequence 3 arrives after 4 and 5 but within the window; it must be
	// accepted and the stream must show no loss.
	pkts := []RTPPacket{
		{Packet: rtpPkt(1, 0, false, "a"), Arrival: 0},
		{Packet: rtpPkt(2, 1, false, "b"), Arrival: 1},
		{Packet: rtpPkt(4, 3, false, "d"), Arrival: 2},
		{Packet: rtpPkt(5, 4, false, "e"), Arrival: 3},
		{Packet: rtpPkt(3, 2, false, "c"), Arrival: 4}, // late, within window
	}
	rec := RecoverRTP(pkts)
	if rec.Unrecoverable != 0 || rec.Received != 5 {
		t.Fatalf("late in-window packet should be recovered: %+v", rec)
	}
	if len(rec.Packets) != 5 || rec.Packets[2].Seq != 3 {
		t.Fatalf("late packet not placed in order")
	}
}

func TestRecoverRTPSequenceWrap(t *testing.T) {
	// Sequence wraps from 65534 to 1 across the boundary.
	seqs := []uint16{65533, 65534, 65535, 0, 1, 2}
	var pkts []RTPPacket
	for i, s := range seqs {
		pkts = append(pkts, RTPPacket{Packet: rtpPkt(s, uint32(i*3000), false, "x"), Arrival: i})
	}
	rec := RecoverRTP(pkts)
	if rec.Received != 6 || rec.Unrecoverable != 0 {
		t.Fatalf("wrap misread as loss: %+v", rec)
	}
	for i := 1; i < len(rec.Packets); i++ {
		if rec.Packets[i].Seq != rec.Packets[i-1].Seq+1 {
			t.Fatalf("sequence not continuous across wrap at %d", i)
		}
	}
}

func TestAssembleFramesGroupsByMarkerAndTimestamp(t *testing.T) {
	rec := RTPRecovery{Packets: []RTPPacketExt{
		{Seq: 1, Pkt: rtpPkt(1, 100, false, "aa")},
		{Seq: 2, Pkt: rtpPkt(2, 100, true, "bb")},
		{Seq: 3, Pkt: rtpPkt(3, 200, true, "cc")},
	}}
	frames := AssembleFrames(rec)
	if len(frames) != 2 {
		t.Fatalf("frames = %d", len(frames))
	}
	if string(frames[0].Payload) != "aabb" || frames[0].Timestamp != 100 {
		t.Fatalf("frame 0 = %+v", frames[0])
	}
	if !frames[0].Complete || !frames[1].Complete {
		t.Fatalf("complete flag wrong: %+v", frames)
	}
}

func TestAssembleFramesDetectsLossInsideFrame(t *testing.T) {
	// Frame spans sequence 1..3 but 2 is missing (unrecoverable).
	rec := RTPRecovery{Packets: []RTPPacketExt{
		{Seq: 1, Pkt: rtpPkt(1, 100, false, "a")},
		{Seq: 3, Pkt: rtpPkt(3, 100, true, "c")},
	}}
	frames := AssembleFrames(rec)
	if len(frames) != 1 || frames[0].Complete {
		t.Fatalf("frame across a gap must be incomplete: %+v", frames)
	}
	if frames[0].Missing != 1 {
		t.Fatalf("missing = %d", frames[0].Missing)
	}
}

func TestMediaClockUnwrapsTimestamps(t *testing.T) {
	clock, err := NewMediaClock(timeline.Base{Name: "v", TimeScale: 90000}, (1<<32)-3000)
	if err != nil {
		t.Fatal(err)
	}
	t1, err := clock.Tick((1 << 32) - 3000) // zero point
	if err != nil || t1 != 0 {
		t.Fatalf("first tick = %d (%v)", t1, err)
	}
	t2, err := clock.Tick(3000) // wrapped
	if err != nil {
		t.Fatal(err)
	}
	if t2 != 6000 {
		t.Fatalf("wrapped tick = %d, want 6000", t2)
	}
}
