// Package rtp recovers damaged transport streams using a bounded playout
// window. Reordered packets that fill an observed gap count as recovered;
// duplicate packets are dropped and never counted as recovery; gaps that
// survive the window are reported unrecoverable.
package rtp

import (
	"math"
	"sort"

	"github.com/pion/rtp"

	"mediaforensics/internal/model"
	"mediaforensics/internal/rtpstats"
)

// DefaultWindow is the maximum forward distance (in sequence numbers) across
// which a missing packet may still arrive and be recovered.
const DefaultWindow = 64

// Stats is re-exported from rtpstats so callers keep a single import path
// while the validator depends only on the stable statistics type.
type Stats = rtpstats.Stats

// Recoverer ingests RTP wire packets in arrival order.
type Recoverer struct {
	windowSize int
	frameStepMs float64

	received  map[uint16]bool
	gaps      map[uint16]int // sequence -> arrival index when the gap opened
	firstSeq  uint16
	haveFirst bool
	maxSeq    uint16
	haveMax   bool
	arrivals  int

	latencies []float64
	stats     Stats
	errors    []*model.ForensicError
}

// NewRecoverer builds a recoverer. frameStep/clockRate define one packet
// slot's duration for latency reporting; clockRate 0 disables conversion.
func NewRecoverer(windowSize int, frameStep, clockRate uint32) *Recoverer {
	if windowSize <= 0 {
		windowSize = DefaultWindow
	}
	ms := 0.0
	if clockRate > 0 {
		ms = float64(frameStep) * 1000.0 / float64(clockRate)
	}
	return &Recoverer{
		windowSize:  windowSize,
		frameStepMs: ms,
		received:    map[uint16]bool{},
		gaps:        map[uint16]int{},
	}
}

// Ingest parses one wire packet through pion/rtp and updates recovery state.
func (r *Recoverer) Ingest(raw []byte) error {
	r.arrivals++
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(raw); err != nil {
		r.stats.Malformed++
		r.stats.Dropped++
		r.errors = append(r.errors, model.NewError(model.StageRTPRecover, "malformed",
			-1, -1, "rtp parse failure: "+err.Error()))
		return err
	}
	s := pkt.SequenceNumber
	if r.received[s] {
		// Duplicate: explicitly dropped, never credited as recovered.
		r.stats.Duplicates++
		r.stats.Dropped++
		return nil
	}
	r.received[s] = true
	r.stats.Received++
	if !r.haveFirst {
		r.firstSeq, r.haveFirst = s, true
	}
	if !r.haveMax {
		r.maxSeq, r.haveMax = s, true
		r.stats.InOrder++
		return nil
	}
	win := uint16(r.windowSize)
	fwd := forwardDist(r.maxSeq, s)
	switch {
	case fwd >= 1 && fwd <= win:
		// Forward packet within the window; intermediate sequences become
		// open gaps that later arrivals may fill.
		r.stats.InOrder++
		r.advanceMax(s)
	case fwd == 0:
		// Already covered by the duplicate check; defensive only.
	default:
		// The sequence is behind the max: either an in-window late packet
		// (candidate recovery) or a packet far outside the playout window.
		back := forwardDist(s, r.maxSeq)
		if back >= 1 && back <= win {
			if opened, isGap := r.gaps[s]; isGap {
				// The sequence had been observed missing and now arrives
				// inside the window: a genuine recovery.
				r.stats.Recovered++
				r.latencies = append(r.latencies,
					float64(r.arrivals-opened)*r.frameStepMs)
				delete(r.gaps, s)
			} else {
				r.stats.InOrder++
			}
			// Late packets never move the high-water mark.
		} else {
			// Large forward jump (modular distance beyond the window):
			// intermediate gaps are registered and immediately expire.
			r.stats.InOrder++
			r.advanceMax(s)
		}
	}
	return nil
}

// advanceMax registers every missing sequence between the previous max and
// the new high-water mark as an open gap, then expires gaps outside window.
func (r *Recoverer) advanceMax(s uint16) {
	if !r.haveMax || s == r.maxSeq {
		r.maxSeq = s
		r.haveMax = true
		return
	}
	d := forwardDist(r.maxSeq, s)
	for i := uint16(1); i < d; i++ {
		g := r.maxSeq + i
		if !r.received[g] {
			if _, seen := r.gaps[g]; !seen {
				r.gaps[g] = r.arrivals
			}
		}
	}
	r.maxSeq = s
	r.expireGaps()
}

func (r *Recoverer) expireGaps() {
	for g := range r.gaps {
		if forwardDist(g, r.maxSeq) > uint16(r.windowSize) {
			r.stats.Unrecoverable++
			r.errors = append(r.errors, model.NewError(model.StageRTPRecover, "window_loss",
				-1, -1, "sequence exceeded playout window"))
			delete(r.gaps, g)
		}
	}
}

// Close finalizes a stream: gaps never filled are unrecoverable and the
// latency distribution is summarized.
func (r *Recoverer) Close() (Stats, []*model.ForensicError) {
	r.stats.Unrecoverable += len(r.gaps)
	for range r.gaps {
		r.errors = append(r.errors, model.NewError(model.StageRTPRecover, "end_of_stream_loss",
			-1, -1, "sequence missing at stream close"))
	}
	r.gaps = map[uint16]int{}
	if r.haveFirst && r.haveMax {
		r.stats.Expected = int(forwardDist(r.firstSeq, r.maxSeq)) + 1
	}
	r.stats.P95LatencyMs = percentile95(r.latencies)
	return r.stats, r.errors
}

// Errors returns the forensic errors recorded so far.
func (r *Recoverer) Errors() []*model.ForensicError { return r.errors }

// forwardDist returns modular distance a->b in [0,65535], handling uint16 wrap.
func forwardDist(a, b uint16) uint16 { return b - a }

// percentile95 uses nearest-rank on sorted recovered-packet latencies.
func percentile95(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	rank := int(math.Ceil(0.95 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}
