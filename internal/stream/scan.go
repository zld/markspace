package stream

import (
	"mediacheck/internal/fault"
	"mediacheck/internal/rtp"
)

// Sample is one access unit: the per-sample row the acceptance report is
// built from. Byte and time fields use the unified coordinate systems
// (reassembled payload bytes, expanded media ticks).
type Sample struct {
	Index       int
	Codec       Codec
	KeyFrame    bool
	NALTypes    []uint32
	FirstSeq    uint64
	LastSeq     uint64
	Byte        fault.ByteRange
	Time        fault.TimeRange
	Duration    int64 // ticks until the next sample's start
	Contributed []NALUnit
}

// ScanResult is the full output of scanning one packetized track.
type ScanResult struct {
	Codec   Codec
	Samples []Sample
	Faults  []fault.Fault
}

// Scanner groups NAL units into access units using RTP marker bits and
// timestamp changes, then hands them to keyframe/damage analysis.
type Scanner struct {
	codec     Codec
	assembler *Assembler
	rec       *fault.Recorder
	pending   []NALUnit
	samples   []Sample
	lastStart int64
	haveTime  bool
}

// NewScanner wires an assembler and fault recorder for one codec.
func NewScanner(codec Codec) *Scanner {
	rec := fault.NewRecorder()
	return &Scanner{
		codec:     codec,
		assembler: NewAssembler(codec, rec),
		rec:       rec,
	}
}

// Push feeds one ordered packet through assembly and sample grouping.
func (s *Scanner) Push(op rtp.OrderedPacket) {
	ts := int64(op.Packet.Timestamp)
	// A timestamp change closes any fully assembled units that belonged
	// to the previous access unit, even when a marker was missing.
	if s.haveTime && ts != s.lastStart {
		s.closeSampleAt(s.lastStart)
	}
	s.haveTime = true
	s.lastStart = ts

	ev := s.assembler.Push(op)
	for _, n := range ev.Completed {
		s.pending = append(s.pending, n)
	}
	if op.Packet.Marker {
		s.closeSampleAt(ts)
	}
}

// Flush ends the track: any dangling fragment becomes a truncated-NAL
// fault and a final partial sample (if NALs survived) is emitted.
func (s *Scanner) Flush() ScanResult {
	s.assembler.Flush()
	if len(s.pending) > 0 {
		s.closeSampleAt(s.lastStart)
	}
	s.finalizeDurations()
	return ScanResult{
		Codec:   s.codec,
		Samples: s.samples,
		Faults:  s.rec.All(),
	}
}

func (s *Scanner) closeSample(endTS uint32) {
	s.closeSampleAt(int64(endTS))
}

func (s *Scanner) closeSampleAt(endTime int64) {
	if len(s.pending) == 0 {
		return
	}
	nals := s.pending
	s.pending = nil

	first, last := nals[0], nals[len(nals)-1]
	key := false
	types := make([]uint32, 0, len(nals))
	for _, n := range nals {
		types = append(types, n.Type)
		if n.Kind == nalIDR {
			key = true
		}
	}
	idx := len(s.samples)
	s.samples = append(s.samples, Sample{
		Index:       idx,
		Codec:       s.codec,
		KeyFrame:    key,
		NALTypes:    types,
		FirstSeq:    first.FirstSeq,
		LastSeq:     last.LastSeq,
		Byte:        fault.ByteRange{Offset: first.ByteOff, Length: last.ByteOff + last.ByteLen - first.ByteOff},
		Time:        fault.TimeRange{Start: first.TimeStart, End: endTime},
		Contributed: nals,
	})
}

func (s *Scanner) finalizeDurations() {
	for i := range s.samples {
		if i+1 < len(s.samples) {
			s.samples[i].Duration = s.samples[i+1].Time.Start - s.samples[i].Time.Start
		} else if len(s.samples) > 1 {
			// Last sample inherits the previous frame duration.
			s.samples[i].Duration = s.samples[i-1].Duration
		}
	}
}
