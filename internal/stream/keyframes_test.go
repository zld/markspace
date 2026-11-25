package stream

import "testing"

func TestKeyframeIntervals(t *testing.T) {
	s := NewScanner(CodecH264)
	// IDR at t=0, P frames, second IDR at t=14400 => 4 samples, 4 ticks.
	s.Push(mkOrdered(0, 0, mkPacket(0, true, []byte{0x65, 1})))
	s.Push(mkOrdered(1, 1, mkPacket(3600, true, []byte{0x41, 2})))
	s.Push(mkOrdered(2, 2, mkPacket(7200, true, []byte{0x41, 3})))
	s.Push(mkOrdered(3, 3, mkPacket(10800, true, []byte{0x41, 4})))
	s.Push(mkOrdered(4, 4, mkPacket(14400, true, []byte{0x65, 5})))
	res := s.Flush()

	rep := AnalyzeKeyframes(res)
	if len(rep.KeyIndices) != 2 || rep.KeyIndices[0] != 0 || rep.KeyIndices[1] != 4 {
		t.Fatalf("key indices = %v", rep.KeyIndices)
	}
	if len(rep.Intervals) != 1 {
		t.Fatalf("intervals = %v", rep.Intervals)
	}
	iv := rep.Intervals[0]
	if iv.Ticks != 14400 || iv.SamplesBetween != 4 {
		t.Fatalf("interval = %+v, want ticks=14400 samples=4", iv)
	}
	if rep.MaxTicks != 14400 || rep.MaxSamples != 4 {
		t.Fatalf("max interval wrong: %+v", rep)
	}
}

func TestKeyframeReportWithoutEnoughKeys(t *testing.T) {
	s := NewScanner(CodecH264)
	s.Push(mkOrdered(0, 0, mkPacket(0, true, []byte{0x65, 1})))
	s.Push(mkOrdered(1, 1, mkPacket(3600, true, []byte{0x41, 2})))
	res := s.Flush()
	rep := AnalyzeKeyframes(res)
	if len(rep.KeyIndices) != 1 || len(rep.Intervals) != 0 {
		t.Fatalf("single key frame yields no intervals: %+v", rep)
	}
}
