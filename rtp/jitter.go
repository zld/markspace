// Package rtp reorders RTP packets, discards duplicates and recovers missing
// sequence numbers inside a bounded waiting window. It wraps github.com/
// pion/rtp for packet parsing and shares the media clock/error model.
package rtp

import (
	"medkernel/media"

	prtp "github.com/pion/rtp"
)

// Packet is the jitter-buffer view of one RTP packet.
type Packet struct {
	Seq       uint16
	Timestamp uint32
	SSRC      uint32
	Marker    bool
	Payload   []byte
	Order     int64 // monotonic receive order, set by Push
	Raw       *prtp.Packet
}

// FromPion converts a pion packet into the internal representation.
func FromPion(p *prtp.Packet) Packet {
	return Packet{
		Seq: p.SequenceNumber, Timestamp: p.Timestamp, SSRC: p.SSRC,
		Marker: p.Marker, Payload: append([]byte(nil), p.Payload...), Raw: p,
	}
}

// SeqRange is a contiguous run of missing extended sequence numbers.
type SeqRange struct {
	From int64
	To   int64 // inclusive
}

// Stats summarizes loss/recovery/latency behavior.
type Stats struct {
	Received   int64
	Duplicates int64
	Recovered  int64 // gaps filled by a late packet inside the window
	Loss       int64 // gaps declared unrecoverable past the window
	MaxGap     int
	LossRanges []SeqRange
	P95Latency int64 // 95th-percentile receive-to-release delay, in packet slots
}

// JitterBuffer reorders packets by extended 16-bit sequence number. Memory is
// bounded by Window: at most Window packets plus the outstanding-gap set are
// held, and gaps older than the window are retired immediately.
type JitterBuffer struct {
	Window      int // tolerated out-of-order distance before a gap is declared loss
	held        map[int64]Packet
	outstanding map[int64]bool // missing sequence numbers still inside the window
	nextOut     int64
	anchor      int64 // extended seq of the first packet ever received
	started     bool
	recvN       int64
	latency     []int64
	stats       Stats
}

// NewJitterBuffer builds a buffer with the given reorder window (>= 1).
func NewJitterBuffer(window int) *JitterBuffer {
	if window < 1 {
		window = 1
	}
	return &JitterBuffer{Window: window, held: map[int64]Packet{}, outstanding: map[int64]bool{}}
}

// Push accepts one packet. Duplicate sequence numbers are counted and dropped.
func (b *JitterBuffer) Push(p Packet) error {
	if p.Raw == nil {
		return media.New(media.ClassRTP, "rtp.jitter", "nil underlying RTP packet")
	}
	b.recvN++
	if !b.started {
		b.anchor = int64(p.Seq)
		b.nextOut = b.anchor
		b.started = true
	}
	ext := media.Extend16(b.anchor, p.Seq)
	if _, exists := b.held[ext]; exists {
		b.stats.Duplicates++
		return media.New(media.ClassContinuity, "rtp.jitter",
			"duplicate RTP sequence number").WithIndex(ext)
	}
	if ext < b.nextOut {
		// Already released as part of a loss declaration: treat as late dup.
		b.stats.Duplicates++
		return media.New(media.ClassContinuity, "rtp.jitter",
			"late packet after loss window").WithIndex(ext)
	}
	p.Order = b.recvN
	if b.outstanding[ext] {
		// The missing slot is filled before its window expired: recovery.
		delete(b.outstanding, ext)
		b.stats.Recovered++
	}
	b.held[ext] = p
	b.stats.Received++
	return nil
}

// Flush releases every in-order packet available, opening gaps as needed and
// declaring loss for gaps whose distance exceeds the Window. It never
// fabricates media: missing sequence numbers only update counters.
func (b *JitterBuffer) Flush() []Packet {
	var out []Packet
	for {
		if p, ok := b.held[b.nextOut]; ok {
			b.latency = append(b.latency, b.recvN-p.Order)
			out = append(out, p)
			delete(b.held, b.nextOut)
			b.nextOut++
			continue
		}
		minHeld, has := b.minHeld()
		if !has {
			return out
		}
		// Every slot between the output cursor and the next held packet is
		// an outstanding gap until its distance exceeds the window.
		for seq := b.nextOut; seq < minHeld; seq++ {
			b.outstanding[seq] = true
		}
		if int(minHeld-b.nextOut) >= b.Window {
			b.declareLoss(b.nextOut, minHeld-1)
			b.nextOut = minHeld
			continue
		}
		return out // inside the window: keep waiting for a late packet
	}
}

// Drain emits all remaining held packets at stream end, declaring every
// still-open gap as loss so no held packet is stranded.
func (b *JitterBuffer) Drain() []Packet {
	var out []Packet
	for {
		minHeld, has := b.minHeld()
		if !has {
			return out
		}
		if minHeld > b.nextOut {
			b.declareLoss(b.nextOut, minHeld-1)
			b.nextOut = minHeld
		}
		p := b.held[b.nextOut]
		b.latency = append(b.latency, b.recvN-p.Order)
		out = append(out, p)
		delete(b.held, b.nextOut)
		b.nextOut++
	}
}

func (b *JitterBuffer) minHeld() (int64, bool) {
	first := true
	var min int64
	for k := range b.held {
		if first || k < min {
			min, first = k, false
		}
	}
	return min, !first
}

func (b *JitterBuffer) declareLoss(from, to int64) {
	for seq := from; seq <= to; seq++ {
		delete(b.outstanding, seq)
		b.stats.Loss++
	}
	gap := int(to - from + 1)
	if gap > b.stats.MaxGap {
		b.stats.MaxGap = gap
	}
	if n := len(b.stats.LossRanges); n > 0 && b.stats.LossRanges[n-1].To+1 == from {
		b.stats.LossRanges[n-1].To = to
	} else {
		b.stats.LossRanges = append(b.stats.LossRanges, SeqRange{From: from, To: to})
	}
}

// Stats returns the running statistics including the p95 release latency
// measured in receive-order slots.
func (b *JitterBuffer) Stats() Stats {
	st := b.stats
	st.P95Latency = percentile95(b.latency)
	return st
}

// RecoveryRatePercent reports recovered gaps as a percentage of all gaps
// (recovered + lost), in the closed range [0, 100]; 100 when there were none.
func (b *JitterBuffer) RecoveryRatePercent() float64 {
	total := b.stats.Recovered + b.stats.Loss
	if total == 0 {
		return 100
	}
	return float64(b.stats.Recovered) * 100 / float64(total)
}

func percentile95(sorted []int64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	// latency is appended in release order, not sorted; copy and sort.
	cp := append([]int64(nil), sorted...)
	for i := 1; i < len(cp); i++ {
		v := cp[i]
		j := i - 1
		for j >= 0 && cp[j] > v {
			cp[j+1] = cp[j]
			j--
		}
		cp[j+1] = v
	}
	idx := (len(cp)*95)/100 - 1
	if idx < 0 {
		idx = 0
	}
	return cp[idx]
}
