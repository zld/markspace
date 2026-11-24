// Package rtp recovers ordered media from RTP sequence numbers.
//
// The recoverer never fabricates payloads. A missing sequence is held as an
// open gap while it remains inside the reorder window; if a later arrival
// fills it, it counts as recovered. Once the gap is older than the window it
// is charged as unrecovered and late data is not inserted into ordered output.
package rtp

import (
	pionrtp "github.com/pion/rtp"

	"github.com/zld/avpipe/internal/fault"
)

// Packet is the concrete RTP packet type used throughout avpipe.
type Packet = pionrtp.Packet

// Config controls sequence recovery and local memory use.
type Config struct {
	// ReorderWindow is the maximum sequence distance retained for a gap.
	ReorderWindow uint16 `json:"reorder_window"`
	// MaxBufferedBytes bounds payload plus 12-byte RTP header accounting.
	// Zero uses the default; a negative value disables enforcement.
	MaxBufferedBytes int `json:"max_buffered_bytes"`
}

// DefaultConfig returns a small mobile-friendly reorder buffer.
func DefaultConfig() Config {
	return Config{ReorderWindow: 32, MaxBufferedBytes: 4 << 20}
}

func (c Config) withDefaults() Config {
	if c.ReorderWindow == 0 {
		c.ReorderWindow = 32
	}
	if c.MaxBufferedBytes == 0 {
		c.MaxBufferedBytes = 4 << 20
	}
	return c
}

// Stats contains deterministic sequence recovery counters.
type Stats struct {
	Input             int `json:"input"`
	Duplicates        int `json:"duplicates"`
	OutOfOrder        int `json:"out_of_order"`
	GapsDetected      int `json:"gaps_detected"`
	RecoveredPackets  int `json:"recovered_packets"`
	UnrecoveredGaps   int `json:"unrecovered_gaps"`
	LateAfterWindow   int `json:"late_after_window"`
	DeliveredPackets  int `json:"delivered_packets"`
	BufferedBytesPeak int `json:"buffered_bytes_peak"`
}

// TotalLossGaps is every unique sequence gap whose outcome is known.
func (s Stats) TotalLossGaps() int { return s.RecoveredPackets + s.UnrecoveredGaps }

// RecoveryRate returns recovered gaps divided by resolved gaps. A stream with
// no gaps has rate 1.0. The result is always in [0,1].
func (s Stats) RecoveryRate() float64 {
	total := s.TotalLossGaps()
	if total == 0 {
		return 1
	}
	rate := float64(s.RecoveredPackets) / float64(total)
	if rate < 0 {
		return 0
	}
	if rate > 1 {
		return 1
	}
	return rate
}

// Recoverer is a stateful RTP sequence reorder and gap tracker.
type Recoverer struct {
	cfg      Config
	started  bool
	base     int64
	maxExt   int64
	seen     map[int64]struct{}
	openGaps map[int64]struct{}
	expired  map[int64]struct{}
	buffer   map[int64]*Packet
	ordered  []*Packet
	stats    Stats
	bufBytes int
}

// NewRecoverer creates a recoverer.
func NewRecoverer(cfg Config) *Recoverer {
	cfg = cfg.withDefaults()
	return &Recoverer{
		cfg:      cfg,
		seen:     make(map[int64]struct{}),
		openGaps: make(map[int64]struct{}),
		expired:  make(map[int64]struct{}),
		buffer:   make(map[int64]*Packet),
	}
}

// Stats returns a copy of the current statistics.
func (r *Recoverer) Stats() Stats {
	r.stats.DeliveredPackets = len(r.ordered)
	return r.stats
}

// Ordered returns packets in contiguous sequence order after Flush.
func (r *Recoverer) Ordered() []*Packet { return r.ordered }

// Add ingests one arrival. Duplicates and late packets are counted but do not
// create additional media output.
func (r *Recoverer) Add(p *Packet) error {
	if p == nil {
		return fault.New(fault.KindInvalid, "nil RTP packet")
	}
	r.stats.Input++

	if !r.started {
		ext := int64(p.SequenceNumber)
		r.started = true
		r.base = ext
		r.maxExt = ext
		r.seen[ext] = struct{}{}
		r.buffer[ext] = p
		r.bufBytes += wireSize(p)
		r.advance()
		return nil
	}

	ext := unwrapSequence(p.SequenceNumber, r.maxExt)
	if _, duplicate := r.seen[ext]; duplicate {
		r.stats.Duplicates++
		return nil
	}
	r.seen[ext] = struct{}{}

	// A sequence below base was already delivered or expired as unrecoverable.
	if ext < r.base {
		if _, late := r.expired[ext]; late {
			r.stats.LateAfterWindow++
		}
		return nil
	}

	if ext > r.maxExt {
		for missing := r.maxExt + 1; missing < ext; missing++ {
			if _, exists := r.seen[missing]; exists {
				continue
			}
			if _, open := r.openGaps[missing]; !open {
				r.openGaps[missing] = struct{}{}
				r.stats.GapsDetected++
			}
		}
		r.maxExt = ext
	} else if ext < r.maxExt {
		r.stats.OutOfOrder++
	}

	if _, open := r.openGaps[ext]; open {
		delete(r.openGaps, ext)
		r.stats.RecoveredPackets++
	}

	size := wireSize(p)
	if r.cfg.MaxBufferedBytes >= 0 && r.cfg.MaxBufferedBytes > 0 &&
		r.bufBytes+size > r.cfg.MaxBufferedBytes {
		r.advance()
		if r.bufBytes+size > r.cfg.MaxBufferedBytes {
			return fault.New(fault.KindResource,
				"RTP reorder buffer exceeds %d bytes", r.cfg.MaxBufferedBytes)
		}
	}

	r.buffer[ext] = p
	r.bufBytes += size
	if r.bufBytes > r.stats.BufferedBytesPeak {
		r.stats.BufferedBytesPeak = r.bufBytes
	}
	r.advance()
	return nil
}

// Flush closes all gaps at end-of-stream. Missing sequences are charged as
// unrecovered; no zeroed or synthetic packets are appended.
func (r *Recoverer) Flush() []*Packet {
	if !r.started {
		return nil
	}
	for r.base <= r.maxExt {
		if p, ok := r.buffer[r.base]; ok {
			r.ordered = append(r.ordered, p)
			r.bufBytes -= wireSize(p)
			delete(r.buffer, r.base)
			r.base++
			continue
		}
		if _, open := r.openGaps[r.base]; open {
			delete(r.openGaps, r.base)
			r.stats.UnrecoveredGaps++
		}
		r.expired[r.base] = struct{}{}
		r.base++
	}
	r.stats.DeliveredPackets = len(r.ordered)
	return r.ordered
}

// advance delivers contiguous packets and expires gaps older than the window.
func (r *Recoverer) advance() {
	window := int64(r.cfg.ReorderWindow)
	for r.base <= r.maxExt {
		if p, ok := r.buffer[r.base]; ok {
			r.ordered = append(r.ordered, p)
			r.bufBytes -= wireSize(p)
			delete(r.buffer, r.base)
			r.base++
			continue
		}

		// The highest sequence has moved at least a full window beyond base.
		if r.maxExt-r.base < window {
			break
		}
		if _, open := r.openGaps[r.base]; open {
			delete(r.openGaps, r.base)
			r.stats.UnrecoveredGaps++
		}
		r.expired[r.base] = struct{}{}
		r.base++
	}
	r.stats.DeliveredPackets = len(r.ordered)
}

func wireSize(p *Packet) int {
	if p == nil {
		return 0
	}
	return 12 + len(p.Payload)
}

// unwrapSequence maps a uint16 RTP sequence onto the int64 timeline near ref.
func unwrapSequence(seq uint16, ref int64) int64 {
	refLow := uint16(uint64(ref) & 0xffff)
	delta := int16(seq - refLow)
	return ref + int64(delta)
}
