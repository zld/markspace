// Package rtp analyses timestamped RTP delivery records and classifies every
// packet as in-order, duplicated, reordered, recovered (late within a
// configurable recovery window) or permanently lost. Sequence numbers use
// uint16 modular arithmetic, so 16-bit wraparound is handled natively.
//
// Committed statistics are never rewritten: once a gap ages past the recovery
// window it is finalized as loss, and a later arrival cannot change it.
package rtp

import (
	"medtrace/internal/mederr"
	"medtrace/internal/timeline"

	pionrtp "github.com/pion/rtp"
)

// Record is one timestamped RTP delivery observation.
type Record struct {
	ArrivalNs  timeline.Timestamp
	Packet     *pionrtp.Packet
	SSRC       uint32
	Sequence   uint16
	RTPTime    uint32
	Marker     bool
	PayloadLen int
}

// Stats is the deterministic per-SSRC delivery summary.
type Stats struct {
	SSRC          uint32
	Received      int64 // arrivals counted, including duplicates
	Unique        int64 // distinct sequence numbers seen
	Duplicates    int64
	Reordered     int64
	Lost          int64 // gaps finalized outside the recovery window
	Recovered     int64 // gaps filled by a late packet inside the window
	PendingGaps   int64 // still inside the window at Flush time
	FirstSequence uint16
	LastSequence  uint16
}

// RecoveryRate returns recovered/(recovered+lost); zero when no gaps existed.
func (s Stats) RecoveryRate() float64 {
	denom := s.Recovered + s.Lost
	if denom == 0 {
		return 0
	}
	return float64(s.Recovered) / float64(denom)
}

type gap struct {
	ssrc     uint32
	seq      uint16
	openedNs timeline.Timestamp
	expired  bool
}

type streamState struct {
	stats   Stats
	seen    map[uint16]bool
	pending map[uint16]*gap
	started bool
}

// Analyzer keeps bounded per-SSRC state and processes records in arrival
// order. Memory is proportional to outstanding gaps plus the seen window.
type Analyzer struct {
	WindowNs      int64
	streams       map[uint32]*streamState
	SeenWindowMax int // cap on remembered sequence numbers per stream
}

// NewAnalyzer builds an analyzer whose recovery window is windowNs.
func NewAnalyzer(windowNs int64) (*Analyzer, error) {
	if windowNs <= 0 {
		return nil, mederr.New(mederr.CatInvalidInput, "recovery window must be positive, got %d", windowNs)
	}
	return &Analyzer{
		WindowNs:      windowNs,
		streams:       map[uint32]*streamState{},
		SeenWindowMax: 1 << 16,
	}, nil
}

// modDist returns (a-b) modulo 2^16 as a signed value in [-32768, 32767].
func modDist(a, b uint16) int32 {
	d := int32(uint16(a - b))
	if d > 32767 {
		d -= 65536
	}
	return d
}

func (a *Analyzer) state(ssrc uint32) *streamState {
	s, ok := a.streams[ssrc]
	if !ok {
		s = &streamState{seen: map[uint16]bool{}, pending: map[uint16]*gap{}}
		s.stats.SSRC = ssrc
		a.streams[ssrc] = s
	}
	return s
}

// AddPacket classifies one parsed RTP packet that arrived at arrivalNs.
func (a *Analyzer) AddPacket(arrivalNs timeline.Timestamp, p *pionrtp.Packet) error {
	if p == nil {
		return mederr.New(mederr.CatInvalidInput, "nil RTP packet")
	}
	return a.add(Record{
		ArrivalNs:  arrivalNs,
		Packet:     p,
		SSRC:       p.SSRC,
		Sequence:   p.SequenceNumber,
		RTPTime:    p.Timestamp,
		Marker:     p.Marker,
		PayloadLen: len(p.Payload),
	})
}

// AddRecord classifies one externally decoded record.
func (a *Analyzer) AddRecord(rec Record) error {
	return a.add(rec)
}

func (a *Analyzer) add(rec Record) error {
	s := a.state(rec.SSRC)
	s.stats.Received++

	// Finalize any pending gaps older than the recovery window before
	// classifying the new arrival.
	a.expire(rec.SSRC, rec.ArrivalNs)

	if !s.started {
		s.started = true
		s.stats.FirstSequence = rec.Sequence
		s.seen[rec.Sequence] = true
		s.stats.Unique = 1
		s.stats.LastSequence = rec.Sequence
		return nil
	}

	if s.seen[rec.Sequence] {
		// Repeat of a sequence number already delivered.
		s.stats.Duplicates++
		return nil
	}

	d := modDist(rec.Sequence, s.stats.LastSequence)
	if g, ok := s.pending[rec.Sequence]; ok {
		// Fills an open gap: recovered inside the window (expired ones have
		// already been removed from the pending map).
		if !g.expired {
			s.stats.Recovered++
			delete(s.pending, rec.Sequence)
		}
		s.stats.Reordered++
	} else if d < 0 {
		// Older than the highest delivered seq and never gapped: reordered.
		s.stats.Reordered++
	}
	s.seen[rec.Sequence] = true
	s.stats.Unique++

	if d > 0 {
		// Forward jump: every missing sequence becomes a pending gap.
		for delta := int32(1); delta < d; delta++ {
			missing := uint16(int32(rec.Sequence) - (d - delta))
			if !s.seen[missing] {
				s.pending[missing] = &gap{ssrc: rec.SSRC, seq: missing, openedNs: rec.ArrivalNs}
			}
		}
		s.stats.LastSequence = rec.Sequence
	}
	return nil
}

// expire moves pending gaps older than the recovery window into finalized
// loss. Expired gaps leave the pending map so late arrivals cannot rewrite
// the committed loss statistic.
func (a *Analyzer) expire(ssrc uint32, now timeline.Timestamp) {
	s := a.streams[ssrc]
	for seq, g := range s.pending {
		if int64(now)-int64(g.openedNs) >= a.WindowNs {
			g.expired = true
			s.stats.Lost++
			delete(s.pending, seq)
		}
	}
}

// Flush finalizes the stream at end of input: gaps still inside the window
// are counted as lost because no further arrivals can recover them.
func (a *Analyzer) Flush() []Stats {
	out := make([]Stats, 0, len(a.streams))
	for ssrc, s := range a.streams {
		for _, g := range s.pending {
			if !g.expired {
				s.stats.Lost++
			}
		}
		s.stats.PendingGaps = 0
		s.pending = map[uint16]*gap{}
		out = append(out, s.stats)
		_ = ssrc
	}
	return out
}

// StatsFor returns the current (non-finalized) stats for one SSRC.
func (a *Analyzer) StatsFor(ssrc uint32) (Stats, bool) {
	s, ok := a.streams[ssrc]
	if !ok {
		return Stats{}, false
	}
	st := s.stats
	st.PendingGaps = int64(len(s.pending))
	return st, true
}
