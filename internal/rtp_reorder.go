package kernel

import (
	"fmt"
	"time"

	"github.com/pion/rtp"
)

const (
	// MaxPacketBytes rejects implausibly large network/TS frames up front.
	MaxPacketBytes = 1 << 20
	// DefaultReorderWindow is the accepted RTP sequence span.
	DefaultReorderWindow uint16 = 64
)

// ReorderConfig controls deterministic RTP reordering.
type ReorderConfig struct {
	Window       uint16
	BandwidthBps int
	MaxBytes     int
}

// ReorderStats counts transport decisions without retaining packet contents.
type ReorderStats struct {
	Received   int `json:"received"`
	Duplicates int `json:"duplicates"`
	Released   int `json:"released"`
	Late       int `json:"late"`
	Lost       int `json:"lost"`
}

// Reorderer deduplicates and orders RTP packets by 16-bit sequence number.
type Reorderer struct {
	cfg    ReorderConfig
	stats  ReorderStats
	next   uint64
	maxExt uint64
	ready  bool

	pending map[uint64]*rtp.Packet
	seen    map[uint64]struct{}
}

// NewReorderer constructs a windowed RTP reorder buffer.
func NewReorderer(cfg ReorderConfig) *Reorderer {
	if cfg.Window == 0 {
		cfg.Window = DefaultReorderWindow
	}
	if cfg.MaxBytes <= 0 || cfg.MaxBytes > MaxPacketBytes {
		cfg.MaxBytes = MaxPacketBytes
	}
	if cfg.BandwidthBps <= 0 {
		cfg.BandwidthBps = 4_000_000
	}
	return &Reorderer{
		cfg:     cfg,
		pending: make(map[uint64]*rtp.Packet),
		seen:    make(map[uint64]struct{}),
	}
}

// Stats returns a copy of the counters.
func (r *Reorderer) Stats() ReorderStats { return r.stats }

// UpdateBandwidth smooths a bandwidth sample and returns the new estimate.
func (r *Reorderer) UpdateBandwidth(payloadBytes int, elapsed time.Duration) int {
	if payloadBytes <= 0 || elapsed <= 0 {
		return r.cfg.BandwidthBps
	}
	sample := int(float64(payloadBytes*8) / elapsed.Seconds())
	if sample <= 0 {
		return r.cfg.BandwidthBps
	}
	if r.cfg.BandwidthBps == 0 {
		r.cfg.BandwidthBps = sample
	} else {
		r.cfg.BandwidthBps = (3*r.cfg.BandwidthBps + sample) / 4
	}
	return r.cfg.BandwidthBps
}

// WaitDuration is the adaptive hole wait. Higher bandwidth yields a shorter
// wait because a fixed sequence window should arrive in less wall time.
func (r *Reorderer) WaitDuration() time.Duration {
	windowBytes := float64(r.cfg.Window) * 188 * 8
	d := time.Duration(windowBytes * float64(time.Second) / float64(r.cfg.BandwidthBps))
	if d < 20*time.Millisecond {
		return 20 * time.Millisecond
	}
	if d > 500*time.Millisecond {
		return 500 * time.Millisecond
	}
	return d
}

// Ingest parses and inserts one raw RTP datagram. It returns only newly
// contiguous packets; callers never observe a packet twice.
func (r *Reorderer) Ingest(raw []byte, sourcePacket uint64) ([]*rtp.Packet, []GapEvent, error) {
	r.stats.Received++
	if len(raw) > r.cfg.MaxBytes {
		return nil, nil, &MediaError{
			Stage:       "ingest",
			Code:        "packet_too_large",
			Message:     fmt.Sprintf("packet length %d exceeds limit %d", len(raw), r.cfg.MaxBytes),
			PID:         0xFFFF,
			Packet:      sourcePacket,
			Recoverable: false,
		}
	}
	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw); err != nil {
		return nil, nil, &MediaError{
			Stage:       "ingest",
			Code:        "invalid_rtp",
			Message:     "malformed RTP packet",
			PID:         0xFFFF,
			Packet:      sourcePacket,
			Recoverable: false,
			Err:         err,
		}
	}

	ext := r.extend(pkt.Header.SequenceNumber)
	if !r.ready {
		r.ready = true
		r.next = ext
		r.maxExt = ext
	}
	if ext > r.maxExt {
		r.maxExt = ext
	}

	if _, dup := r.seen[ext]; dup {
		r.stats.Duplicates++
		return nil, nil, nil
	}
	if _, buffered := r.pending[ext]; buffered {
		r.stats.Duplicates++
		return nil, nil, nil
	}

	var gaps []GapEvent
	if ext < r.next {
		age := r.next - ext
		if age > uint64(r.cfg.Window) {
			r.stats.Late++
			gaps = append(gaps, GapEvent{
				ID:           fmt.Sprintf("rtp-late-%d", ext),
				Kind:         GapRTPLate,
				PID:          0xFFFF,
				Time:         rtpTimeRange(&pkt),
				FirstPacket:  sourcePacket,
				LastPacket:   sourcePacket,
				MissingCount: 1,
				Detail:       fmt.Sprintf("sequence %d arrived %d positions after commit", pkt.SequenceNumber, age),
				Recoverable:  true,
			})
		}
		// Committed order cannot change. Recent late packets are deliberately
		// not reintroduced; FlushLosses already accounted for their hole.
		r.seen[ext] = struct{}{}
		r.pruneSeen()
		return nil, gaps, nil
	}

	r.pending[ext] = &pkt
	released := r.drain()
	r.stats.Released += len(released)
	r.pruneSeen()
	return released, gaps, nil
}

func (r *Reorderer) drain() []*rtp.Packet {
	var out []*rtp.Packet
	for {
		pkt, ok := r.pending[r.next]
		if !ok {
			break
		}
		delete(r.pending, r.next)
		r.seen[r.next] = struct{}{}
		out = append(out, pkt)
		r.next++
	}
	return out
}

// FlushLosses releases all buffered packets in order and records remaining
// sequence holes. It never synthesizes packet payloads.
func (r *Reorderer) FlushLosses(firstSourcePacket uint64) ([]*rtp.Packet, []GapEvent) {
	if !r.ready {
		return nil, nil
	}
	var out []*rtp.Packet
	var gaps []GapEvent
	holeStart := uint64(0)
	holeCount := 0
	flushHole := func(end uint64) {
		if holeCount == 0 {
			return
		}
		seq := uint16(holeStart)
		gaps = append(gaps, GapEvent{
			ID:           fmt.Sprintf("rtp-gap-%d", seq),
			Kind:         GapRTPSequence,
			PID:          0xFFFF,
			Time:         TimeRange{},
			FirstPacket:  firstSourcePacket,
			LastPacket:   firstSourcePacket,
			MissingCount: holeCount,
			Detail:       fmt.Sprintf("RTP sequence hole beginning %d through ext %d", seq, end),
			Recoverable:  true,
		})
		r.stats.Lost += holeCount
		holeCount = 0
	}

	for seq := r.next; seq <= r.maxExt && r.ready; seq++ {
		if pkt, ok := r.pending[seq]; ok {
			flushHole(seq - 1)
			delete(r.pending, seq)
			r.seen[seq] = struct{}{}
			out = append(out, pkt)
			r.next = seq + 1
		} else {
			if holeCount == 0 {
				holeStart = seq
			}
			holeCount++
			r.next = seq + 1
		}
		if seq == ^uint64(0) {
			break
		}
	}
	flushHole(r.maxExt)
	r.stats.Released += len(out)
	r.pruneSeen()
	return out, gaps
}

func (r *Reorderer) extend(seq uint16) uint64 {
	if !r.ready {
		return uint64(seq)
	}
	base := r.maxExt &^ 0xffff
	candidate := base | uint64(seq)
	const half = uint64(1) << 15
	if candidate+half < r.maxExt {
		candidate += 1 << 16
	} else if candidate > r.maxExt+half {
		candidate -= 1 << 16
	}
	return candidate
}

func (r *Reorderer) pruneSeen() {
	cutoff := uint64(0)
	if r.next > uint64(r.cfg.Window)*2 {
		cutoff = r.next - uint64(r.cfg.Window)*2
	}
	for seq := range r.seen {
		if seq < cutoff {
			delete(r.seen, seq)
		}
	}
}

func rtpTimeRange(pkt *rtp.Packet) TimeRange {
	ts := uint64(pkt.Header.Timestamp)
	return TimeRange{Start90k: ts, End90k: ts + 1}
}
