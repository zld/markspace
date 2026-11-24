package repair

import (
	"testing"

	"github.com/pion/rtp"

	"mediasync/internal/clock"
	msrtp "mediasync/internal/rtp"
)

func frame(seq uint16, ssrc uint32, ext int64, payloadSize int) msrtp.Frame {
	return msrtp.Frame{
		Packet: &rtp.Packet{
			Header:  rtp.Header{SequenceNumber: seq, SSRC: ssrc, Timestamp: uint32(ext)},
			Payload: make([]byte, payloadSize),
		},
		ExtendedTS: ext,
		EmitIndex:  uint64(seq),
	}
}

func TestTimelineMonotonicRecovery(t *testing.T) {
	b := NewTimelineBuilder(7, clock.DefaultClockRate, clock.MaxPacketBytes)
	keyEvery := uint16(3)
	for seq := uint16(1); seq <= 6; seq++ {
		ext := int64(seq) * 9000 // 100 ms per frame at 90 kHz
		_, err := b.AddFrame(frame(seq, 7, ext, 128), seq%keyEvery == 1, false)
		if err != nil {
			t.Fatalf("seq %d rejected: %v", seq, err)
		}
	}
	samples := b.Samples()
	if len(samples) != 6 {
		t.Fatalf("samples=%d want 6", len(samples))
	}
	for i := 1; i < len(samples); i++ {
		if samples[i].MediaNS <= samples[i-1].MediaNS {
			t.Fatalf("media time not monotonic at %d", i)
		}
	}
	if !samples[0].IsKeyframe || samples[1].IsKeyframe {
		t.Fatal("keyframe marking incorrect")
	}
}

func TestTimelineIsolatesTruncatedAndBackwards(t *testing.T) {
	b := NewTimelineBuilder(7, clock.DefaultClockRate, clock.MaxPacketBytes)
	if _, err := b.AddFrame(frame(1, 7, 9000, 100), true, false); err != nil {
		t.Fatal(err)
	}
	// Truncated payload must be isolated, not indexed.
	if _, err := b.AddFrame(frame(2, 7, 18000, 50), false, true); err == nil {
		t.Fatal("truncated frame accepted into timeline")
	}
	// Backwards RTP timestamp must be isolated and reported with the seq.
	if _, err := b.AddFrame(frame(3, 7, 8000, 100), false, false); err == nil {
		t.Fatal("backwards frame accepted")
	}
	c := b.Counters()
	if c.Isolated != 2 || c.RejectedBoundary != 2 {
		t.Fatalf("isolation counters wrong: %+v", c)
	}
	if c.FirstFailSeq != 2 || c.FirstFailKind != clock.ErrTruncated {
		t.Fatalf("first failure not captured: seq=%d kind=%s", c.FirstFailSeq, c.FirstFailKind)
	}
	if len(b.Samples()) != 1 {
		t.Fatal("isolated samples leaked into indexable timeline")
	}
}

func TestTimelineSyntheticGapFill(t *testing.T) {
	b := NewTimelineBuilder(7, clock.DefaultClockRate, clock.MaxPacketBytes)
	b.AddFrame(frame(1, 7, 9000, 100), true, false)
	b.AddFrame(frame(2, 7, 18000, 100), false, false)
	fill := b.FillGap(3, 27000) // seq 3 was lost on the network
	if !fill.Synthetic || !fill.Isolated {
		t.Fatal("gap fill not marked synthetic/isolated")
	}
	b.AddFrame(frame(4, 7, 36000, 100), false, false)
	if got := b.Counters().SyntheticFills; got != 1 {
		t.Fatalf("synthetic fills=%d want 1", got)
	}
}

func TestTimelineOversizedPayloadRejected(t *testing.T) {
	b := NewTimelineBuilder(7, clock.DefaultClockRate, 64)
	_, err := b.AddFrame(frame(1, 7, 9000, 65), true, false)
	if err == nil || err.Kind != clock.ErrTruncated {
		t.Fatalf("oversized payload classification wrong: %v", err)
	}
}
