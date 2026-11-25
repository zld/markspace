package clock

import (
	"errors"
	"testing"

	"mediad/internal/model"

	"github.com/pion/rtp"
)

func TestPCRMonotonicAndWrap(t *testing.T) {
	tl := NewTimeline()
	step := model.ClockHz / 1000 // one millisecond per sample
	// Start just before the PCR period boundary so normal step increments
	// reach the wrap without an unrelated large forward jump.
	first := pcrModulus - 2*step
	media, _, err := tl.FeedPCR(first, 0)
	if err != nil || media != 0 {
		t.Fatalf("first sample media=%d err=%v", media, err)
	}
	media, _, err = tl.FeedPCR(first+step, 188)
	if err != nil || media != step {
		t.Fatalf("second sample media=%d err=%v", media, err)
	}
	preWrap := media
	next := int64(0) // wrapped back to the start of a new PCR period
	media, wrapped, err := tl.FeedPCR(next, 376)
	if err != nil || !wrapped {
		t.Fatalf("wrap not detected: wrapped=%v err=%v", wrapped, err)
	}
	if media != preWrap+step {
		t.Fatalf("media after wrap = %d, want %d", media, preWrap+step)
	}
	if got := tl.Events(); len(got) != 1 || got[0].Kind != "wrap" {
		t.Fatalf("events = %+v", got)
	}
	// Out-of-range PCR must be rejected rather than invented.
	if _, _, err = tl.FeedPCR(pcrModulus, 564); err == nil {
		t.Fatalf("expected pcr corruption error, got nil")
	}
}

func TestPCRBackwardJumpStaysMonotonic(t *testing.T) {
	tl := NewTimeline()
	tl.FeedPCR(1_000_000, 0)
	m1, _, _ := tl.FeedPCR(1_027_000, 188)
	// A small backward move (not a modular wrap) must not rewind media.
	m2, wrapped, _ := tl.FeedPCR(1_010_000, 376)
	if wrapped || m2 < m1 {
		t.Fatalf("backward jump moved media backwards: %d -> %d", m1, m2)
	}
	if ev := tl.Events(); len(ev) != 1 || ev[0].Kind != "jump" {
		t.Fatalf("expected jump event, got %+v", ev)
	}
}

func TestCorruptionSpans(t *testing.T) {
	tl := NewTimeline()
	tl.FeedPCR(0, 0)
	tl.FeedPCR(27_000, 188) // 1 ms
	tl.MarkCorruption(188, 0x200)
	if pids := tl.AffectedPIDs(0); len(pids) != 0 {
		t.Fatalf("span must not apply before its start: %v", pids)
	}
	if pids := tl.AffectedPIDs(27_000); len(pids) != 1 || pids[0] != 0x200 {
		t.Fatalf("expected pid 0x200 affected, got %v", pids)
	}
}

func TestRTPSequenceGapAndTimestampWrap(t *testing.T) {
	c := NewRTPClock()
	mk := func(seq uint16, ts uint32) []byte {
		h := rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           42,
		}
		raw, err := h.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	m, err := c.Ingest(mk(10, 1000), 0)
	if err != nil || m != 0 {
		t.Fatalf("first RTP sample: media=%d err=%v", m, err)
	}
	// Skip sequences 11 and 12.
	if _, err = c.Ingest(mk(13, 4000), 188); err != nil {
		t.Fatal(err)
	}
	if c.LostPackets() != 2 {
		t.Fatalf("lost = %d, want 2", c.LostPackets())
	}
	// Timestamp near 32-bit boundary: 0xFFFFFFF0 -> 0x00000010.
	m1, _ := c.Ingest(mk(14, 0xFFFFFFF0), 376)
	m2, _ := c.Ingest(mk(15, 0x00000010), 564)
	if m2 <= m1 {
		t.Fatalf("RTP wrap must keep media monotonic: %d -> %d", m1, m2)
	}
	if m2-m1 != int64(0x20)*rtpToMediaTicks {
		t.Fatalf("wrapped delta = %d", m2-m1)
	}
}

func TestRTPBadHeader(t *testing.T) {
	c := NewRTPClock()
	_, err := c.Ingest([]byte{0x00}, 7)
	var ce *model.CorruptError
	if !errors.As(err, &ce) || ce.Offset != 7 || ce.Kind != "rtp" {
		t.Fatalf("expected offset-7 rtp CorruptError, got %v", err)
	}
}
