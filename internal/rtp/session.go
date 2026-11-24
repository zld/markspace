package rtp

import (
	"sort"

	"github.com/local/medialab/internal/model"
	"github.com/local/medialab/internal/timeline"
	"github.com/pion/rtp"
)

// LatencyStats summarizes per-packet buffering delay on the master clock.
type LatencyStats struct {
	Count  int
	MaxMs  int64
	P95Ms  int64
	MeanMs int64
}

// SessionResult is the full outcome of feeding a perturbed packet stream
// through reorder and repair.
type SessionResult struct {
	Delivered     []*rtp.Packet
	Missing       []uint16
	RecoveredSeqs []uint16
	Recovery      RecoveryResult
	Stats         ReorderStats
	Latency       LatencyStats
}

// Session drives one simulated network delivery. BandwidthKbps is a
// cyclic instantaneous bandwidth schedule; each pushed media packet
// consumes the next value and converts it into a bounded buffer wait, so
// no packet can ever wait without a finite ceiling.
type Session struct {
	buf       *ReorderBuffer
	rec       *Recoverer
	cfg       ReorderConfig
	bandwidth []int
	bwIdx     int

	groupBase map[uint16]uint32   // media seq -> group base timestamp
	groupSeqs map[uint32][]uint16 // base ts -> media members
	// repairQueue holds parity packets that arrived before their group's
	// surviving media; they are retried after each later media push.
	repairQueue map[uint32]*rtp.Packet
	delivered   map[uint16]*rtp.Packet
	latencies   []int64
	arrival     map[uint16]int64 // original arrival tick per media seq
}

// NewSession constructs a session.
func NewSession(cfg ReorderConfig, bandwidthKbps []int) *Session {
	return &Session{
		buf:         NewReorderBuffer(cfg),
		rec:         NewRecoverer(),
		cfg:         cfg,
		bandwidth:   bandwidthKbps,
		groupBase:   map[uint16]uint32{},
		groupSeqs:   map[uint32][]uint16{},
		repairQueue: map[uint32]*rtp.Packet{},
		delivered:   map[uint16]*rtp.Packet{},
		arrival:     map[uint16]int64{},
	}
}

// RTPSSRCConst mirrors the fixture session SSRC for rebuilt packets.
func RTPSSRCConst() uint32 { return 0x4D4C4231 }

// Push feeds one arriving packet with its simulated arrival tick.
func (s *Session) Push(pkt *rtp.Packet, arriveTick int64) {
	if pkt.PayloadType == 97 {
		s.handleRepair(pkt, arriveTick)
		return
	}
	// The group base timestamp is the first media timestamp of each
	// 4-packet group; media packets within a group step by RTPFrameStep.
	baseTS := pkt.Timestamp - uint32(pkt.SequenceNumber%4)*3000
	s.groupBase[pkt.SequenceNumber] = baseTS
	s.groupSeqs[baseTS] = append(s.groupSeqs[baseTS], pkt.SequenceNumber)
	s.rec.ObserveMedia(baseTS, pkt.SequenceNumber, pkt.Payload)
	s.arrival[pkt.SequenceNumber] = arriveTick

	wait := s.nextWait()
	s.buf.Push(pkt.SequenceNumber, pkt.Timestamp, pkt.Payload, false, arriveTick)
	s.drainAvailable(arriveTick + wait)
	// A parity packet queued ahead of its media may now be actionable.
	s.retryQueuedRepairs(arriveTick)
}

// handleRepair registers the parity packet. If its group cannot be
// resolved yet (media still in flight), the packet is queued and retried
// after each subsequent media arrival.
func (s *Session) handleRepair(pkt *rtp.Packet, arriveTick int64) {
	baseTS := pkt.Timestamp
	s.ensureGroupMembers(baseTS)
	s.repairQueue[baseTS] = pkt
	s.retryQueuedRepairs(arriveTick)
}

// ensureGroupMembers fills groupSeqs from timestamp arithmetic so a repair
// packet arriving first still knows which four media seqs it protects.
func (s *Session) ensureGroupMembers(baseTS uint32) {
	if _, ok := s.groupSeqs[baseTS]; ok {
		return
	}
	// Every group spans 4 media timestamps of 3000 ticks each.
	groupIndex := (baseTS - 90000) / (4 * 3000)
	var seqs []uint16
	for k := uint32(0); k < 4; k++ {
		seq := uint16(groupIndex*4 + k)
		seqs = append(seqs, seq)
		s.groupBase[seq] = baseTS
	}
	s.groupSeqs[baseTS] = seqs
}

// retryQueuedRepairs attempts reconstruction for every queued parity
// packet whose group now has exactly one outstanding hole. Groups whose
// surviving media has not fully arrived stay queued.
func (s *Session) retryQueuedRepairs(arriveTick int64) {
	for baseTS, pkt := range s.repairQueue {
		// Membership may have been learned from media arrivals after the
		// repair packet; re-derive the full 4-seq group from its base TS.
		seqs := s.fullGroupSeqs(baseTS)
		s.groupSeqs[baseTS] = seqs
		for _, seq := range seqs {
			if _, ok := s.groupBase[seq]; !ok {
				s.groupBase[seq] = baseTS
			}
		}
		s.rec.AddRepair(baseTS, seqs, pkt.Payload)
		var missing []uint16
		present := 0
		for _, seq := range seqs {
			if _, delivered := s.delivered[seq]; delivered {
				present++
				continue
			}
			if s.buf.hasSeq(seq) {
				present++
				continue
			}
			missing = append(missing, seq)
		}
		switch {
		case len(missing) == 0:
			delete(s.repairQueue, baseTS) // complete group, nothing to repair
		case len(missing) == 1 && present == len(seqs)-1:
			out, _ := s.rec.Recover(nil, missing, func(seq uint16) (uint32, bool) {
				ts, ok := s.groupBase[seq]
				return ts, ok
			})
			for seq, payload := range out {
				s.buf.Push(seq, baseTS, payload, false, arriveTick)
				s.drainAvailable(arriveTick + s.cfg.WaitForBandwidth(1000))
			}
			delete(s.repairQueue, baseTS)
			// Otherwise more media may still arrive: keep the parity queued.
		}
	}
}

// fullGroupSeqs returns the four media sequence numbers of the group
// starting at baseTS, derived from the fixed 90kHz/30fps/4-packet layout.
func (s *Session) fullGroupSeqs(baseTS uint32) []uint16 {
	groupIndex := (baseTS - 90000) / (4 * 3000)
	seqs := make([]uint16, 4)
	for k := uint32(0); k < 4; k++ {
		seqs[k] = uint16(groupIndex*4 + k)
	}
	return seqs
}

// Finalize declares any window-external losses, gives queued parity
// packets a final reconstruction chance, flushes remaining packets and
// returns the ordered result with all counters.
func (s *Session) Finalize(book *model.ErrorBook) SessionResult {
	// Last repair pass once every surviving packet has been observed.
	s.retryQueuedRepairs(0)

	lost := s.buf.AdvanceWindow(book, func(seq uint16) timeline.Window {
		return timeline.Window{Start: int64(seq) * 100, End: int64(seq)*100 + 100}
	})
	s.drainAvailable(0)

	res := SessionResult{Stats: s.buf.Stats()}
	res.Missing = append(res.Missing, lost...)

	var allSeqs []uint16
	for seq := range s.groupBase {
		allSeqs = append(allSeqs, seq)
	}
	sortSeqsFrom(allSeqs)
	for _, seq := range allSeqs {
		if _, ok := s.delivered[seq]; !ok {
			res.Missing = appendUnique(res.Missing, seq)
		}
	}
	for seq := range s.delivered {
		res.RecoveredSeqs = append(res.RecoveredSeqs, seq)
	}
	// Recovered count is reconstructed packets only: those delivered whose
	// original arrival was never observed.
	var recovered []uint16
	for _, seq := range allSeqs {
		if _, observed := s.arrival[seq]; !observed {
			if _, ok := s.delivered[seq]; ok {
				recovered = append(recovered, seq)
			}
		}
	}
	res.RecoveredSeqs = recovered
	res.Recovery = RecoveryResult{
		Recovered:   len(recovered),
		Unrecovered: len(res.Missing),
	}

	for _, seq := range allSeqs {
		if p, ok := s.delivered[seq]; ok {
			res.Delivered = append(res.Delivered, p)
		}
	}
	res.Latency = computeLatency(s.latencies)
	return res
}

func (s *Session) drainAvailable(releaseTick int64) {
	for _, a := range s.buf.Drain() {
		latency := releaseTick - a.arriveTick
		if latency < 0 {
			latency = 0
		}
		s.latencies = append(s.latencies, latency)
		s.delivered[a.seq] = &rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 96, SequenceNumber: a.seq,
				Timestamp: a.ts, SSRC: RTPSSRCConst(),
			},
			Payload: a.payload,
		}
	}
}

func (s *Session) nextWait() int64 {
	if len(s.bandwidth) == 0 {
		return s.cfg.BaseWaitTicks
	}
	bw := s.bandwidth[s.bwIdx%len(s.bandwidth)]
	s.bwIdx++
	return s.cfg.WaitForBandwidth(bw)
}

func computeLatency(latencies []int64) LatencyStats {
	if len(latencies) == 0 {
		return LatencyStats{}
	}
	sorted := append([]int64(nil), latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var sum int64
	for _, v := range sorted {
		sum += v
	}
	p95Idx := (len(sorted) * 95) / 100
	if p95Idx >= len(sorted) {
		p95Idx = len(sorted) - 1
	}
	return LatencyStats{
		Count:  len(sorted),
		MaxMs:  timeline.Millis(sorted[len(sorted)-1]),
		P95Ms:  timeline.Millis(sorted[p95Idx]),
		MeanMs: timeline.Millis(sum / int64(len(sorted))),
	}
}

func appendUnique(xs []uint16, v uint16) []uint16 {
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	return append(xs, v)
}

func sortSeqsFrom(seqs []uint16) {
	if len(seqs) == 0 {
		return
	}
	base := seqs[0]
	sort.Slice(seqs, func(i, j int) bool {
		return timeline.Advance16(base, seqs[i]) < timeline.Advance16(base, seqs[j])
	})
}
