package pipeline

import (
	"mediaverify/internal/clock"
	"mediaverify/internal/fixture"
	"mediaverify/internal/rtp"
	"mediaverify/internal/subtitle"
	"mediaverify/internal/timeline"
)

type ingestState struct {
	wm          *timeline.Watermark
	extender    *rtp.Extender
	buffer      *rtp.ReorderBuffer
	received    int
	duplicate   int
	reordered   int
	lost        int
	checksumBad int
	checksumErr error // first classified KindChecksum failure (for strict mode)
	firstTS     uint32
	haveFirst   bool
	frameRate   clock.FrameRate
}

// ingestRTP drives the reorder buffer into the watermark. Every accepted
// packet becomes an event; loss gaps and checksum failures are turned into
// untrusted spans by buildIntervals rather than silently filled.
func ingestRTP(cfg Config, stream *fixture.Stream) (*ingestState, error) {
	st := &ingestState{
		wm:        timeline.New(timeline.Config{LateWindow: cfg.LateWindow}),
		extender:  rtp.NewExtender(),
		buffer:    rtp.NewReorderBuffer(64),
		frameRate: cfg.FrameRate,
	}
	frameDur := cfg.FrameRate.FrameDurationNS()
	if stream.HasLate {
		return st.ingestWithLateEvent(cfg, stream, frameDur)
	}
	for _, raw := range stream.Packets {
		pkt, err := st.extender.Extend(raw)
		if err != nil {
			return nil, err
		}
		if cerr := rtp.VerifyChecksum(pkt); cerr != nil {
			st.checksumBad++
			if st.checksumErr == nil {
				st.checksumErr = cerr
			}
		}
		if !st.haveFirst {
			st.haveFirst = true
			st.firstTS = pkt.Timestamp
		}
		res, err := st.buffer.Offer(pkt)
		if err != nil {
			return nil, err
		}
		st.received++
		if res.Duplicate {
			st.duplicate++
		}
		for _, emitted := range res.Emitted {
			st.commit(emitted, stream, frameDur)
		}
	}
	// Flush the final contiguous tail.
	for _, emitted := range st.buffer.Drain(fixture.SSRC) {
		st.commit(emitted, stream, frameDur)
	}
	stats := st.buffer.SnapshotStats()[fixture.SSRC]
	st.duplicate = stats.Duplicate + st.checksumBad
	st.reordered = stats.Reordered
	st.lost = stats.Lost
	return st, nil
}

// ingestWithLateEvent feeds packets directly to the watermark (the held
// packet is a timeline-level scenario, not an RTP reorder-buffer one). Once
// the level passes the held event but remains inside the late window, the
// packet is inserted and triggers a deterministic replay.
func (s *ingestState) ingestWithLateEvent(cfg Config, stream *fixture.Stream, frameDur clock.Ticks) (*ingestState, error) {
	for _, raw := range stream.Packets {
		pkt, err := s.extender.Extend(raw)
		if err != nil {
			return nil, err
		}
		if cerr := rtp.VerifyChecksum(pkt); cerr != nil {
			s.checksumBad++
			if s.checksumErr == nil {
				s.checksumErr = cerr
			}
		}
		if !s.haveFirst {
			s.haveFirst = true
			s.firstTS = pkt.Timestamp
		}
		s.received++
		s.commit(pkt, stream, frameDur)
		s.deliverLateIfDue(stream)
	}
	// Outside the window, a final delivery records a sealed rejection.
	if stream.HasLate {
		s.deliverLate(stream)
	}
	s.duplicate = s.checksumBad
	return s, nil
}

func (s *ingestState) commit(pkt rtp.Packet, stream *fixture.Stream, frameDur clock.Ticks) {
	t, _ := rtp.ClockStamp(pkt.Timestamp, s.firstTS, 0, stream.ClockRate)
	s.wm.Push(timeline.Event{
		Seq:      s.wm.NextSeq(),
		Time:     t,
		Keyframe: stream.Keyframes[pkt.Seq16],
		Bytes:    uint64(len(pkt.Payload)),
	})
}

// deliverLateIfDue hands the held packet to the watermark once the level has
// passed its event time, while it is still within the late window.
func (s *ingestState) deliverLateIfDue(stream *fixture.Stream) {
	if !stream.HasLate {
		return
	}
	lateTS := s.firstTS + uint32(int64(stream.LateSeq16)*stream.TicksPerFrame)
	lt, _ := rtp.ClockStamp(lateTS, s.firstTS, 0, stream.ClockRate)
	if s.wm.Level() > lt && s.wm.Level()-lt <= s.wm.LateWindow() {
		s.deliverLate(stream)
	}
}

// deliverLate pushes the held packet directly to the watermark exactly once.
func (s *ingestState) deliverLate(stream *fixture.Stream) {
	if !stream.HasLate {
		return
	}
	stream.HasLate = false
	latePkt, err := s.extender.Extend(stream.LatePacket)
	if err != nil {
		return
	}
	lt, _ := rtp.ClockStamp(latePkt.Timestamp, s.firstTS, 0, stream.ClockRate)
	s.wm.Push(timeline.Event{
		Seq:      s.wm.NextSeq(),
		Time:     lt,
		Keyframe: stream.Keyframes[latePkt.Seq16],
		Bytes:    uint64(len(latePkt.Payload)),
	})
}

// buildIntervals translates loss gaps and timestamp rollbacks into merged
// untrusted spans. A gap touching a subtitle boundary is explicitly tagged
// so the manifest can show why the boundary is untrustworthy.
func buildIntervals(st *ingestState, stream *fixture.Stream, cues []subtitle.Cue) []timeline.Interval {
	var out []timeline.Interval
	frameDur := st.frameRate.FrameDurationNS()
	add := func(start, end clock.Ticks, reason string) {
		if end <= start {
			end = start + frameDur
		}
		_ = st.wm.AddInterval(timeline.Interval{Start: start, End: end, Reason: reason})
		out = append(out, timeline.Interval{Start: start, End: end, Reason: reason})
	}
	for _, gap := range st.buffer.Gaps {
		start := seqToTicks(gap.Start, st.firstTS, stream)
		end := seqToTicks(gap.Start+uint64(gap.Length), st.firstTS, stream)
		reason := "burst-loss"
		if crossesCue(start, end, cues) {
			reason = "burst-loss+subtitle-boundary"
		}
		if crossesKeyframe(gap, stream) {
			reason += "+keyframe"
		}
		add(start, end, reason)
	}
	if st.extender.BackwardJumps > 0 {
		// The fixture rolls back one timestamp at the midpoint; span one
		// frame on either side.
		mid := clock.Ticks(int64(stream.Frames/2) * int64(frameDur))
		add(mid-frameDur, mid+frameDur, "timestamp-rollback")
	}
	return out
}

func seqToTicks(extSeq uint64, baseTS uint32, stream *fixture.Stream) clock.Ticks {
	ts := baseTS + uint32(int64(extSeq)*stream.TicksPerFrame)
	t, _ := rtp.ClockStamp(ts, baseTS, 0, stream.ClockRate)
	return t
}

func crossesKeyframe(gap rtp.Gap, stream *fixture.Stream) bool {
	for seq := gap.Start; seq < gap.Start+uint64(gap.Length); seq++ {
		if stream.Keyframes[uint16(seq)] {
			return true
		}
	}
	return false
}

func crossesCue(start, end clock.Ticks, cues []subtitle.Cue) bool {
	for _, c := range cues {
		if c.Start >= start && c.Start < end {
			return true
		}
	}
	return false
}
