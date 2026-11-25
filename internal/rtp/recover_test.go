package rtp

import (
	"testing"
	"time"

	"github.com/pion/rtp"

	"mediasync/internal/model"
)

func marshalDatagram(t *testing.T, seq uint16, ts uint32, payload []byte) []byte {
	t.Helper()
	p := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: ts, SSRC: 42},
		Payload: payload,
	}
	b, err := p.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestRecoverCorruptPacketLocated(t *testing.T) {
	rec := model.NewRecorder()
	g := NewRecovery(DefaultRecoveryConfig(), rec)
	// A valid packet then a truncated RTP header.
	g.Ingest(CaptureFrame{Data: marshalDatagram(t, 0, 0, []byte("ok")), ByteOffset: 0, Arrival: 0})
	g.Ingest(CaptureFrame{Data: []byte{0x80}, ByteOffset: 1234, Arrival: 1})
	stats := g.Stats()
	if stats.TotalPackets != 2 || stats.Corrupt != 1 {
		t.Fatalf("stats wrong: %+v", stats)
	}
	var found bool
	for _, r := range rec.Records() {
		if r.Code == model.ErrCorruptPacket && r.Offset == 1234 {
			found = true
		}
	}
	if !found {
		t.Fatalf("corrupt packet not located at offset 1234: %v", rec.Records())
	}
	out := g.Pump(true)
	if len(out) != 1 {
		t.Fatalf("valid packet must still be delivered, got %d", len(out))
	}
}

func TestRecoverStreamFullScenario(t *testing.T) {
	rec := model.NewRecorder()
	// Sequence intentionally: reorder (3 before 2), an in-buffer duplicate
	// of 2, then a burst loss of 7.
	seqs := []uint16{0, 1, 3, 2, 2, 4, 5, 6, 8, 9} // 7 is burst-lost
	frames := make([]CaptureFrame, len(seqs))
	var off int64
	for i, s := range seqs {
		data := marshalDatagram(t, s, uint32(s)*160, []byte{byte(s)})
		frames[i] = CaptureFrame{Data: data, ByteOffset: off, Arrival: i}
		off += int64(len(data))
	}
	// Changing bandwidth: wide, then a sharp dip, then recovery.
	bw := []BandwidthSample{
		{T: time.Unix(0, 0), BytesPerSecond: 7_000_000},
		{T: time.Unix(0, 1), BytesPerSecond: 7_000_000},
		{T: time.Unix(0, 2), BytesPerSecond: 100_000},
		{T: time.Unix(0, 3), BytesPerSecond: 80_000},
		{T: time.Unix(0, 4), BytesPerSecond: 5_000_000},
	}
	cfg := DefaultRecoveryConfig()
	// Window long enough to admit the reordering (3,4 after 5) but the
	// burst-lost seq 7 is never filled, so the forced end-of-stream flush
	// declares it.
	cfg.Reorder.WaitWindow = 8
	ordered, stats := RecoverStream(frames, bw, cfg, rec)

	// Ordered output contains every present seq exactly once, sorted.
	want := []uint16{0, 1, 2, 3, 4, 5, 6, 8, 9}
	if len(ordered) != len(want) {
		t.Fatalf("ordered count = %d, want %d (%+v)", len(ordered), len(want), stats)
	}
	for i, p := range ordered {
		if p.Raw.SequenceNumber != want[i] {
			t.Fatalf("position %d got seq %d, want %d", i, p.Raw.SequenceNumber, want[i])
		}
	}
	// With whole-stream buffering the second copy of 2 is still in the
	// reorder buffer, so it is correctly counted as an in-buffer duplicate;
	// post-emission "late" handling is covered separately by TestWaitWindowThenGap.
	if stats.Duplicates != 1 {
		t.Fatalf("duplicates = %d, want 1", stats.Duplicates)
	}
	if stats.Late != 0 {
		t.Fatalf("late = %d, want 0", stats.Late)
	}
	if stats.Missing != 1 || len(stats.Gaps) != 1 || stats.Gaps[0].Start != 7 || stats.Gaps[0].End != 7 {
		t.Fatalf("gap accounting wrong: %+v", stats.Gaps)
	}
	if stats.Emitted != len(want) {
		t.Fatalf("emitted = %d, want %d", stats.Emitted, len(want))
	}
	if stats.HighWater <= 0 {
		t.Fatal("high water mark never recorded")
	}
	if stats.JitterTarget < cfg.Jitter.MinTarget || stats.JitterTarget > cfg.Jitter.MaxTarget {
		t.Fatalf("jitter target out of bounds: %d", stats.JitterTarget)
	}
	if stats.SmoothedBPS <= 0 {
		t.Fatal("smoothed bandwidth not tracked")
	}
	// Gaps are warnings, not hard errors; corrupt frames would be errors.
	if rec.HasError() {
		t.Fatalf("lossy-but-valid stream must not hard-fail: %v", rec.Records())
	}
}

func TestRecoverDeterministic(t *testing.T) {
	rec1, rec2 := model.NewRecorder(), model.NewRecorder()
	seqs := []uint16{10, 12, 11, 13, 15, 14, 16}
	build := func() []CaptureFrame {
		frames := make([]CaptureFrame, len(seqs))
		for i, s := range seqs {
			frames[i] = CaptureFrame{Data: marshalDatagram(t, s, uint32(s), []byte{1}), Arrival: i}
		}
		return frames
	}
	cfg := DefaultRecoveryConfig()
	out1, s1 := RecoverStream(build(), nil, cfg, rec1)
	out2, s2 := RecoverStream(build(), nil, cfg, rec2)
	if len(out1) != len(out2) {
		t.Fatal("non-deterministic output length")
	}
	for i := range out1 {
		if out1[i].ExtSeq != out2[i].ExtSeq {
			t.Fatal("non-deterministic ordering")
		}
	}
	if s1.Emitted != s2.Emitted || s1.HighWater != s2.HighWater || s1.JitterTarget != s2.JitterTarget {
		t.Fatalf("non-deterministic stats: %+v vs %+v", s1, s2)
	}
}

func TestRecoverInBufferDuplicate(t *testing.T) {
	rec := model.NewRecorder()
	g := NewRecovery(DefaultRecoveryConfig(), rec)
	// Hold base at 0 so 3,2 stay buffered; a second 2 is a true in-buffer dup.
	g.Ingest(CaptureFrame{Data: marshalDatagram(t, 0, 0, []byte{0}), Arrival: 0})
	g.Ingest(CaptureFrame{Data: marshalDatagram(t, 3, 0, []byte{3}), Arrival: 1})
	g.Ingest(CaptureFrame{Data: marshalDatagram(t, 2, 0, []byte{2}), Arrival: 2})
	g.Ingest(CaptureFrame{Data: marshalDatagram(t, 2, 0, []byte{2}), Arrival: 3})
	if g.Stats().Duplicates != 1 {
		t.Fatalf("in-buffer duplicates = %d, want 1", g.Stats().Duplicates)
	}
	out := g.Pump(true)
	if len(out) != 3 {
		t.Fatalf("emitted %d, want 3 unique packets", len(out))
	}
}

func TestRecoverWraparoundStream(t *testing.T) {
	rec := model.NewRecorder()
	// Cross the 16-bit boundary with one out-of-order packet.
	seqs := []uint16{65534, 65535, 1, 0}
	frames := make([]CaptureFrame, len(seqs))
	for i, s := range seqs {
		frames[i] = CaptureFrame{Data: marshalDatagram(t, s, 0, []byte{byte(s)}), Arrival: i}
	}
	ordered, _ := RecoverStream(frames, nil, DefaultRecoveryConfig(), rec)
	want := []uint64{65534, 65535, 65536, 65537}
	if len(ordered) != len(want) {
		t.Fatalf("count = %d, want %d", len(ordered), len(want))
	}
	for i, p := range ordered {
		if p.ExtSeq != want[i] {
			t.Fatalf("position %d ext=%d want %d", i, p.ExtSeq, want[i])
		}
	}
}
