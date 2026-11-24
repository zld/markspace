package segment

import (
	"path/filepath"
	"testing"

	"mediapipe/internal/model"
)

func tp(seq uint64, ms int64, payloadLen int) model.Packet {
	return model.Packet{
		ExtSequence: seq,
		MediaMs:     ms,
		FrameMs:     20,
		Payload:     make([]byte, payloadLen),
	}
}

func TestCutterExactBoundariesAndBitrate(t *testing.T) {
	c := NewCutter(40)
	var closed []ClosedSegment
	for i := 0; i < 4; i++ {
		if got := c.Push(tp(uint64(i), int64(i*20), 100)); got != nil {
			closed = append(closed, *got)
		}
	}
	tail := c.Finish(false)
	if tail == nil {
		t.Fatal("expected tail")
	}
	closed = append(closed, *tail)
	if len(closed) != 2 {
		t.Fatalf("segments=%d, want 2", len(closed))
	}
	first := closed[0].Segment
	if first.StartMs != 0 || first.EndMs != 40 || first.PacketCount != 2 {
		t.Fatalf("first segment wrong: %+v", first)
	}
	if first.BitrateBps != 40000 { // 200 bytes * 8 / 0.04s
		t.Fatalf("bitrate=%.1f want 40000", first.BitrateBps)
	}
	if !closed[1].Segment.Complete {
		t.Fatal("normal tail should be complete when it reaches target duration")
	}
}

func TestCutterChoosesNearestPacketBoundary(t *testing.T) {
	c := NewCutter(40)
	times := []int64{0, 20, 30, 50, 70}
	var first *ClosedSegment
	for i, ms := range times {
		if got := c.Push(tp(uint64(i), ms, 10)); got != nil && first == nil {
			first = got
		}
	}
	if first == nil {
		t.Fatal("expected boundary")
	}
	// Target 40 is equidistant from 30 and 50; tie selects the earlier packet.
	if first.Segment.EndMs != 30 {
		t.Fatalf("end=%d want 30", first.Segment.EndMs)
	}
	if first.Segment.BoundaryAlignmentMs != 10 {
		t.Fatalf("alignment=%d want 10", first.Segment.BoundaryAlignmentMs)
	}
}

func TestCutterCleanShortTailIsComplete(t *testing.T) {
	c := NewCutter(2000)
	for i := 0; i < 110; i++ {
		c.Push(tp(uint64(i), int64(i*20), 160))
	}
	tail := c.Finish(false)
	if tail == nil {
		t.Fatal("expected tail")
	}
	if !tail.Segment.Complete {
		t.Fatal("clean EOS short tail should be complete")
	}
	if !tail.Segment.Recoverable {
		t.Fatal("tail should remain recoverable")
	}
}

func TestCutterMarksTruncatedTailIncompleteAndRecoverable(t *testing.T) {
	c := NewCutter(2000)
	c.Push(tp(1, 0, 160))
	c.Push(tp(2, 20, 160))
	got := c.Finish(true)
	if got == nil {
		t.Fatal("expected recoverable tail")
	}
	if got.Segment.Complete {
		t.Fatal("truncated tail must be incomplete")
	}
	if !got.Segment.Recoverable || got.Segment.PacketCount != 2 {
		t.Fatalf("tail not recoverable: %+v", got.Segment)
	}
	if got.Segment.EndMs != 40 {
		t.Fatalf("end=%d want last start + frame duration", got.Segment.EndMs)
	}
}

func TestMP4IndexRoundTripAndCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.mp4")
	in := Index{
		Version:         IndexFormatVersion,
		InputSHA256:     "abc123",
		ProcessedPacket: 128,
		Segments: []model.Segment{{
			Index: 0, StartMs: 0, EndMs: 2000, PacketCount: 100, BitrateBps: 64000, Complete: true,
		}},
	}
	if err := WriteMP4Index(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := ReadMP4Index(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.InputSHA256 != in.InputSHA256 || out.ProcessedPacket != 128 || len(out.Segments) != 1 {
		t.Fatalf("index mismatch: %+v", out)
	}

	if err := AtomicWriteFile(path, []byte("not mp4")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMP4Index(path); err == nil {
		t.Fatal("expected corrupt index read to fail for checkpoint recovery")
	}
}
