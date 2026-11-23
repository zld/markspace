package pipeline

import (
	"mediad/internal/model"
	"mediad/internal/recovery"
	"mediad/internal/rtp"
)

func (s *sim) handleEvent(ev *model.Event) {
	switch ev.Kind {
	case model.KindMalformed:
		s.handleMalformed(ev)
	case model.KindPacket:
		s.onPacket(ev)
	case model.KindSenderReport:
		s.sum.ParsedPackets++
		media := s.mediaOf[ev.SSRC]
		ev.Media = media
		s.est.OnSenderReport(media, ev.NTPMs, ev.RTPTs, ev.ArrivalMs)
	case model.KindReceiverReport:
		s.sum.ParsedPackets++
		s.sum.ReceiverReports++
	case model.KindNACK:
		s.sum.ParsedPackets++
		s.sum.NACKReceived++
		s.onNACK(ev)
	}
}

func (s *sim) handleMalformed(ev *model.Event) {
	s.sum.MalformedPackets++
	s.sum.BufferMalformed++
	if s.sum.MalformedByClass == nil {
		s.sum.MalformedByClass = make(map[string]int)
	}
	s.sum.MalformedByClass[ev.Err.String()]++
	if ev.Err == model.ErrTruncated {
		s.sum.TruncatedTail++
	}
	// The damaged packet's fixed header still identified its stream, so
	// the sync estimator must see it too.
	s.est.OnMalformed(ev.Media)
	if ev.Media != model.MediaUnknown {
		s.sum.SyncMalformed++
	}
}

func (s *sim) onPacket(ev *model.Event) {
	s.sum.ParsedPackets++
	ex := s.ext[ev.SSRC]
	if ex == nil {
		ex = &rtp.Extender{}
		s.ext[ev.SSRC] = ex
	}
	extSeq, class := ex.Extend(ev.Seq16)
	switch class {
	case rtp.SeqDuplicate:
		s.sum.DuplicatePackets++
		return
	case rtp.SeqAmbiguous:
		s.sum.QuarantinedPackets++
		s.quarant++
		if s.sum.QuarantineReasons == nil {
			s.sum.QuarantineReasons = make(map[string]int)
		}
		// The jump is too large to assign a cycle: record how far off it
		// was so the operator can tell a stray sender from corruption.
		delta := int32(int16(ev.Seq16 - uint16(ex.MaxExt())))
		reason := "backward"
		if delta > 0 {
			reason = "forward"
		}
		s.sum.QuarantineReasons[reason]++
		return
	}
	ev.Seq = extSeq
	s.mediaOf[ev.SSRC] = ev.Media
	key := seqKey{ev.SSRC, extSeq}
	if s.emitted[key] || s.accepted[key] {
		s.sum.DuplicatePackets++
		return
	}
	// A reordered packet can still fill a gap that was detected earlier.
	delete(s.missing, key)
	s.trackGap(ev, extSeq)

	if s.wm.LateBy(ev.EventMs) > 0 {
		if s.replay.InsertLate(ev, s.wm) {
			s.markEmitted(key)
			s.est.OnSample(ev)
		}
		return
	}
	s.accepted[key] = true
	s.jit.Push(ev)
}

// trackGap records forward gaps and per-stream pacing for loss estimates.
func (s *sim) trackGap(ev *model.Event, extSeq uint64) {
	if !s.nextSet[ev.SSRC] {
		s.nextSet[ev.SSRC] = true
		s.next[ev.SSRC] = extSeq + 1
		s.lastExt[ev.SSRC] = extSeq
		s.lastEvMs[ev.SSRC] = ev.EventMs
		s.stepMs[ev.SSRC] = stepFor(ev.Media)
		return
	}
	next := s.next[ev.SSRC]
	if extSeq >= next {
		step := s.stepMs[ev.SSRC]
		lastExt := s.lastExt[ev.SSRC]
		lastMs := s.lastEvMs[ev.SSRC]
		for m := next; m < extSeq; m++ {
			if _, ok := s.missing[seqKey{ev.SSRC, m}]; ok {
				continue
			}
			s.missing[seqKey{ev.SSRC, m}] = &lossInfo{
				estEventMs: lastMs + int64(m-lastExt)*step,
			}
		}
		s.next[ev.SSRC] = extSeq + 1
	}
	if extSeq > s.lastExt[ev.SSRC] {
		delta := extSeq - s.lastExt[ev.SSRC]
		span := ev.EventMs - s.lastEvMs[ev.SSRC]
		if span > 0 {
			step := span / int64(delta)
			if step > 0 && step < 1000 {
				s.stepMs[ev.SSRC] = step
			}
		}
		s.lastExt[ev.SSRC] = extSeq
		s.lastEvMs[ev.SSRC] = ev.EventMs
	}
}

func stepFor(m model.MediaType) int64 {
	if m == model.MediaAudio {
		return audioStepMs
	}
	return videoStepMs
}

func (s *sim) onNACK(ev *model.Event) {
	ex := s.ext[ev.SSRC]
	var reqs []recovery.Request
	for _, pid := range ev.NACKSeqs {
		if ex == nil {
			s.rec.Stats.CacheMiss++
			continue
		}
		ext, ok := ex.ResolveFeedback(pid)
		if !ok {
			// Feedback names a packet outside the resolvable horizon:
			// the cache cannot answer it.
			s.rec.Stats.CacheMiss++
			continue
		}
		reqs = append(reqs, recovery.Request{Seq: ext, SSRC: ev.SSRC, Seq16: pid})
	}
	normal, late := s.rec.OnNACK(reqs, func(seq uint64) recovery.LossState {
		return s.lossState(ev.SSRC, seq)
	})
	// A recovered packet reaches the timeline now, not when the sender
	// originally transmitted it; stamping the current arrival keeps both
	// late-window decisions and latency accounting honest.
	for _, r := range normal {
		r.ArrivalMs = ev.ArrivalMs
		s.acceptRecovered(r)
	}
	for _, r := range late {
		r.ArrivalMs = ev.ArrivalMs
		if s.replay.InsertLate(r, s.wm) {
			key := seqKey{r.SSRC, r.Seq}
			delete(s.missing, key)
			s.markEmitted(key)
			s.est.OnSample(r)
		}
	}
}

// lossState classifies a requested sequence number for the recoverer.
func (s *sim) lossState(ssrc uint32, seq uint64) recovery.LossState {
	key := seqKey{ssrc, seq}
	if s.emitted[key] || s.accepted[key] {
		return recovery.StatePresent
	}
	li, ok := s.missing[key]
	if !ok {
		// Never recorded as a gap. If the number is at or beyond the
		// expected next sequence the receiver has simply not observed it
		// yet (tail loss, early feedback); the cache decides whether it
		// can be answered. Otherwise the number is stale.
		if !s.nextSet[ssrc] || seq >= s.next[ssrc] {
			return recovery.StatePending
		}
		return recovery.StateFinalized
	}
	now := s.wm.Current()
	if now > s.wm.FinalizedAt(li.estEventMs) {
		return recovery.StateFinalized
	}
	if now >= li.estEventMs+s.depth {
		li.declared = true
		return recovery.StateDeclared
	}
	return recovery.StatePending
}

func (s *sim) acceptRecovered(ev *model.Event) {
	key := seqKey{ev.SSRC, ev.Seq}
	delete(s.missing, key)
	s.accepted[key] = true
	s.replay.InsertSorted(ev)
	s.markEmitted(key)
	s.est.OnSample(ev)
}

func (s *sim) emit(ev *model.Event) {
	key := seqKey{ev.SSRC, ev.Seq}
	if s.emitted[key] {
		return
	}
	delete(s.accepted, key)
	s.markEmitted(key)
	s.replay.Append(ev)
	s.est.OnSample(ev)
	// Buffering latency: a sample is released when the watermark reaches
	// its presentation time plus the emit depth, so its queue time is
	// (EventMs + depth) - ArrivalMs. Jitter and reorder push the arrival
	// later, shrinking the wait; a sample that arrived early waits longer.
	if ev.Media != model.MediaUnknown {
		lat := ev.EventMs + s.depth - ev.ArrivalMs
		if lat < 0 {
			lat = 0
		}
		s.latencies = append(s.latencies, lat)
	}
}

func (s *sim) markEmitted(key seqKey) {
	s.emitted[key] = true
	n := len(s.emitted)
	if s.onEmit != nil && s.reportEvery > 0 && n%s.reportEvery == 0 {
		s.onEmit(n)
	}
}

// declare marks gaps whose emit deadline has passed.
func (s *sim) declare() {
	now := s.wm.Current()
	for _, li := range s.missing {
		if !li.declared && now >= li.estEventMs+s.depth {
			li.declared = true
		}
	}
}
