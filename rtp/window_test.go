package rtp

import (
	"testing"

	"github.com/pion/rtp"

	"mediarc/model"
	"mediarc/timebase"
)

func newPacket(seq uint16, ts uint32) *rtp.Packet {
	packet := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: seq, Timestamp: ts, SSRC: 0xDEADBEEF}}
	packet.Payload = []byte{byte(seq >> 8), byte(seq)}
	return packet
}

func mustWindow(t *testing.T, depth int) *Window {
	t.Helper()
	w, err := NewWindow(Config{Depth: depth, PacketDurationNS: 20_000_000, ClockRate: 90000})
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	return w
}

func push(t *testing.T, w *Window, packet *rtp.Packet) (Release, error) {
	t.Helper()
	return w.Push(Arrival{Packet: packet, ReceivedNS: int64(packet.SequenceNumber) * 1_000_000})
}

func TestWindowInOrder(t *testing.T) {
	w := mustWindow(t, 4)
	const count = 20
	var frames []Frame
	for i := 0; i < count; i++ {
		rel, err := push(t, w, newPacket(uint16(i), uint32(i)*1800))
		if err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
		frames = append(frames, rel.Frames...)
	}
	rel := w.Flush()
	frames = append(frames, rel.Frames...)
	if len(frames) != count {
		t.Fatalf("delivered %d frames, want %d", len(frames), count)
	}
	for i, frame := range frames {
		if frame.ExtendedSeq != uint64(i) {
			t.Fatalf("frame %d has seq %d", i, frame.ExtendedSeq)
		}
		if frame.StartNS != int64(i)*20_000_000 {
			t.Fatalf("frame %d starts at %d ns, want %d", i, frame.StartNS, int64(i)*20_000_000)
		}
		if frame.OutOfOrder {
			t.Fatalf("frame %d marked out of order", i)
		}
	}
	stats := w.Stats()
	if stats.Duplicates != 0 || stats.Late != 0 || stats.PacketsLost != 0 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestWindowReorderWithinDepth(t *testing.T) {
	w := mustWindow(t, 4)
	order := []uint16{0, 1, 3, 2, 4}
	var frames []Frame
	for _, seq := range order {
		rel, err := push(t, w, newPacket(seq, uint32(seq)*1800))
		if err != nil {
			t.Fatalf("push %d: %v", seq, err)
		}
		frames = append(frames, rel.Frames...)
	}
	rel := w.Flush()
	frames = append(frames, rel.Frames...)
	if len(frames) != 5 {
		t.Fatalf("delivered %d, want 5", len(frames))
	}
	for i, frame := range frames {
		if frame.ExtendedSeq != uint64(i) {
			t.Fatalf("position %d holds seq %d", i, frame.ExtendedSeq)
		}
	}
	if !frames[2].OutOfOrder {
		t.Fatalf("seq 2 should be flagged out of order")
	}
	if got := w.Stats().Reordered; got != 1 {
		t.Fatalf("reordered = %d, want 1", got)
	}
	if got := w.Stats().PacketsLost; got != 0 {
		t.Fatalf("lost = %d, want 0", got)
	}
}

func TestWindowDuplicateIsIdempotent(t *testing.T) {
	w := mustWindow(t, 4)
	if _, err := push(t, w, newPacket(10, 18000)); err != nil {
		t.Fatal(err)
	}
	before := w.Stats()
	rel, err := push(t, w, newPacket(10, 18000))
	if !model.IsCode(err, model.CodeDuplicatePacket) {
		t.Fatalf("duplicate error = %v", err)
	}
	if len(rel.Frames) != 0 || len(rel.Gaps) != 0 {
		t.Fatalf("duplicate released %+v", rel)
	}
	after := w.Stats()
	if after.Duplicates-before.Duplicates != 1 {
		t.Fatalf("duplicate counter: %+v -> %+v", before, after)
	}
	if after.Accepted != before.Accepted {
		t.Fatalf("duplicate changed accepted count")
	}
}

func TestWindowSequenceWrap(t *testing.T) {
	w := mustWindow(t, 8)
	const start = 65530
	var frames []Frame
	for i := 0; i < 20; i++ {
		seq := uint16(start + i)
		rel, err := push(t, w, newPacket(seq, uint32(i)*1800))
		if err != nil {
			t.Fatalf("push %d (seq %d): %v", i, seq, err)
		}
		frames = append(frames, rel.Frames...)
	}
	rel := w.Flush()
	frames = append(frames, rel.Frames...)
	if len(frames) != 20 {
		t.Fatalf("delivered %d frames, want 20", len(frames))
	}
	for i, frame := range frames {
		if frame.ExtendedSeq != uint64(start+i) {
			t.Fatalf("position %d holds extended seq %d, want %d", i, frame.ExtendedSeq, start+i)
		}
	}
	if got := w.Stats().PacketsLost; got != 0 {
		t.Fatalf("wraparound lost %d packets", got)
	}
}

func TestWindowReportsGap(t *testing.T) {
	w := mustWindow(t, 4)
	var gaps []Gap
	for seq := uint16(0); seq < 6; seq++ {
		if seq == 2 || seq == 3 {
			continue
		}
		rel, err := push(t, w, newPacket(seq, uint32(seq)*1800))
		if err != nil {
			t.Fatalf("push %d: %v", seq, err)
		}
		gaps = append(gaps, rel.Gaps...)
	}
	rel := w.Flush()
	gaps = append(gaps, rel.Gaps...)
	if len(gaps) != 1 {
		t.Fatalf("gaps = %+v", gaps)
	}
	if gaps[0].StartSeq != 2 || gaps[0].Count != 2 {
		t.Fatalf("gap = %+v", gaps[0])
	}
	if gaps[0].DurationNS != 40_000_000 {
		t.Fatalf("gap duration = %d, want 40ms", gaps[0].DurationNS)
	}
	if got := w.Stats().PacketsLost; got != 2 {
		t.Fatalf("packets lost = %d, want 2", got)
	}
}

func TestWindowBoundIsHonoured(t *testing.T) {
	w := mustWindow(t, 4)
	// A long forward jump must not be buffered; the window declares the gap.
	rel, err := push(t, w, newPacket(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(rel.Frames) != 1 {
		t.Fatalf("first push released %d frames", len(rel.Frames))
	}
	var gaps []Gap
	for _, seq := range []uint16{1, 2, 3, 4, 5, 9, 10} {
		out, err := push(t, w, newPacket(seq, uint32(seq)*1800))
		if err != nil {
			t.Fatalf("push %d: %v", seq, err)
		}
		gaps = append(gaps, out.Gaps...)
		if resident := len(w.pending); resident > w.Cap() {
			t.Fatalf("window holds %d frames, cap %d", resident, w.Cap())
		}
	}
	rel = w.Flush()
	gaps = append(gaps, rel.Gaps...)
	if len(gaps) == 0 {
		t.Fatal("expected at least one gap")
	}
	var lost uint64
	for _, gap := range gaps {
		lost += gap.Count
	}
	if lost != 3 { // 6, 7, 8 never arrived
		t.Fatalf("lost = %d, want 3", lost)
	}
	if w.MaxResident() > w.Cap() {
		t.Fatalf("max resident %d exceeds cap %d", w.MaxResident(), w.Cap())
	}
}

func TestWindowLatePacketRejected(t *testing.T) {
	w := mustWindow(t, 2)
	for seq := uint16(5); seq < 9; seq++ {
		if _, err := push(t, w, newPacket(seq, uint32(seq)*1800)); err != nil {
			t.Fatalf("push %d: %v", seq, err)
		}
	}
	// Sequence 2 was never delivered and is far behind the floor.
	_, err := push(t, w, newPacket(2, 2*1800))
	if !model.IsCode(err, model.CodeLatePacket) {
		t.Fatalf("late error = %v", err)
	}
	if got := w.Stats().Late; got != 1 {
		t.Fatalf("late = %d", got)
	}
}

func TestWindowTimestampJumpReported(t *testing.T) {
	w, err := NewWindow(Config{Depth: 4, PacketDurationNS: 20_000_000, ClockRate: 90000, TimestampJumps: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := push(t, w, newPacket(0, 0)); err != nil {
		t.Fatal(err)
	}
	// Expected +1800 ticks; 5000 is a jump.
	rel, err := push(t, w, newPacket(1, 5000))
	if err != nil {
		t.Fatalf("jump packet rejected outright: %v", err)
	}
	if len(rel.Issues) != 1 || !model.IsCode(rel.Issues[0], model.CodeTimestampJump) {
		t.Fatalf("issues = %v", rel.Issues)
	}
	// The frame keeps its own timestamp rather than being moved.
	rel2, err := push(t, w, newPacket(2, 5000+1800))
	if err != nil {
		t.Fatal(err)
	}
	_ = rel2
	all := append(rel.Frames, rel2.Frames...)
	all = append(all, w.Flush().Frames...)
	var seq1 *Frame
	for i := range all {
		if all[i].ExtendedSeq == 1 {
			seq1 = &all[i]
		}
	}
	if seq1 == nil {
		t.Fatal("frame 1 missing")
	}
	want, ferr := timebase.NanosFromScale(5000, 90000)
	if ferr != nil {
		t.Fatal(ferr)
	}
	if seq1.StartNS != want {
		t.Fatalf("jumped frame start = %d, want %d", seq1.StartNS, want)
	}
}

func TestWindowConfigurationValidation(t *testing.T) {
	if _, err := NewWindow(Config{Depth: 0}); !model.IsCode(err, model.CodeInvalidArgument) {
		t.Fatalf("zero depth: %v", err)
	}
	if _, err := NewWindow(Config{Depth: 2, PacketDurationNS: -1}); !model.IsCode(err, model.CodeInvalidArgument) {
		t.Fatalf("negative duration: %v", err)
	}
	if _, err := NewWindow(Config{Depth: 2, ClockRate: 0}); err != nil {
		t.Fatalf("default clock rate: %v", err)
	}
	if _, err := w(t).Push(Arrival{}); !model.IsCode(err, model.CodeInvalidRTP) {
		t.Fatalf("nil packet: %v", err)
	}
}

func w(t *testing.T) *Window {
	t.Helper()
	return mustWindow(t, 2)
}
