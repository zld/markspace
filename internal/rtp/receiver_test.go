package rtp

import (
	"testing"

	"github.com/pion/rtp"

	"mediad/internal/model"
)

func makePacket(sn uint16, ts uint32) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: sn,
			Timestamp:      ts,
			SSRC:           0x12345678,
		},
		Payload: []byte{byte(sn), byte(ts)},
	}
}

func feed(t *testing.T, r *Receiver, sns ...uint16) {
	t.Helper()
	for _, sn := range sns {
		r.Write(makePacket(sn, uint32(sn)*160))
	}
}

func TestInOrderAndDuplicate(t *testing.T) {
	r := NewReceiver(8)
	for sn := uint16(0); sn < 5; sn++ {
		out := r.Write(makePacket(sn, 0))
		if out.Classification != ClassInOrder {
			t.Fatalf("sn=%d classified %s", sn, out.Classification)
		}
	}
	out := r.Write(makePacket(2, 0))
	if out.Classification != ClassDuplicate {
		t.Fatalf("repeat sn=2 classified %s, want duplicate", out.Classification)
	}
	st := r.Flush()
	if st.Received != 6 || st.Duplicates != 1 || st.Unrecoverable != 0 {
		t.Fatalf("stats wrong: %+v", st)
	}
}

func TestReorderRecoveredInsideWindow(t *testing.T) {
	r := NewReceiver(8)
	// 0,1,3,2 : sn 2 fills the gap while it is still inside the window.
	out := r.Write(makePacket(0, 0))
	if out.Classification != ClassInOrder {
		t.Fatal(out.Classification)
	}
	feed(t, r, 1, 3) // jump 1->3 opens gap {2}
	out = r.Write(makePacket(2, 0))
	if out.Classification != ClassReordered || out.Recovered != 1 {
		t.Fatalf("late sn=2: %+v, want reordered+recovered", out)
	}
	st := r.Flush()
	if st.RecoveredLoss != 1 || st.Unrecoverable != 0 || st.RecoveryRate != 1.0 {
		t.Fatalf("recovery stats wrong: %+v", st)
	}
	if st.Reordered != 1 {
		t.Fatalf("reordered count = %d, want 1", st.Reordered)
	}
}

func TestLossUnrecoverableOutsideWindow(t *testing.T) {
	r := NewReceiver(4)
	feed(t, r, 0, 10) // opens gaps 1..9
	st := r.Snapshot()
	// Gap 1 is 9 behind highest=10, outside window 4 -> expired already.
	if st.Unrecoverable == 0 || r.OpenGapCount() == 0 {
		t.Fatalf("expected expiry, stats=%+v open=%d", st, r.OpenGapCount())
	}
	// Gap 6 (distance 4) is still recoverable; deliver it now.
	out := r.Write(makePacket(6, 0))
	if out.Classification != ClassReordered || out.Recovered != 1 {
		t.Fatalf("sn=6 inside window: %+v", out)
	}
	// Gap 5 is also within window (distance 5? check boundary): force flush.
	st = r.Flush()
	totalGaps := st.RecoveredLoss + st.Unrecoverable
	if totalGaps != 9 {
		t.Fatalf("nine gaps 1..9 expected, got %d (%+v)", totalGaps, st)
	}
	if st.RecoveryRate >= 1.0 || st.RecoveryRate <= 0 {
		t.Fatalf("partial recovery rate expected, got %f", st.RecoveryRate)
	}
}

func TestWindowBoundary(t *testing.T) {
	r := NewReceiver(4)
	feed(t, r, 0, 5) // gaps 1..4, distances 4,3,2,1 from highest=5
	// Distance exactly equal to window stays recoverable; >window expires.
	st := r.Snapshot()
	if st.Unrecoverable != 0 || r.OpenGapCount() != 4 {
		t.Fatalf("boundary: stats=%+v open=%d (window must be inclusive)", st, r.OpenGapCount())
	}
	feed(t, r, 10) // now gap 1 is distance 9 -> expires
	st = r.Snapshot()
	if st.Unrecoverable == 0 {
		t.Fatal("gap outside window must expire")
	}
}

func TestSequenceWraparound(t *testing.T) {
	r := NewReceiver(8)
	feed(t, r, 65534, 65535, 0, 1) // natural 16-bit wrap, no gaps
	st := r.Flush()
	if st.Unrecoverable != 0 || st.Duplicates != 0 || st.Received != 4 {
		t.Fatalf("wraparound stats wrong: %+v", st)
	}
	// Now a duplicate from before the wrap must be detected.
	if out := r.Write(makePacket(65535, 0)); out.Classification != ClassDuplicate {
		t.Fatalf("pre-wrap duplicate classified %s", out.Classification)
	}
}

func TestTruncatedPacketParseError(t *testing.T) {
	if _, err := Parse([]byte{0x80, 0x60}); err == nil {
		t.Fatal("2-byte buffer must fail parsing")
	} else if !model.HasCode(err, model.ErrRTPTruncated) {
		t.Fatalf("want RTP_TRUNCATED, got %v", err)
	}
	if _, err := Parse(nil); !model.HasCode(err, model.ErrRTPTruncated) {
		t.Fatalf("nil buffer: want RTP_TRUNCATED, got %v", err)
	}
}

func TestReplayDeterministic(t *testing.T) {
	sequence := []uint16{0, 2, 1, 5, 3, 4, 10, 6, 7}
	run := func() Stats {
		r := NewReceiver(6)
		for _, sn := range sequence {
			r.Write(makePacket(sn, uint32(sn)))
		}
		return r.Flush()
	}
	a, b := run(), run()
	if a != b {
		t.Fatalf("replay diverged:\n%+v\n%+v", a, b)
	}
	if a.RecoveryRate < 0 || a.RecoveryRate > 1 {
		t.Fatalf("recovery rate out of range: %f", a.RecoveryRate)
	}
}
