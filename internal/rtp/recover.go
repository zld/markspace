// Package rtp recovers an ordered media timeline from a noisy RTP arrival
// stream: duplicates are removed, a finite window reorders late packets, and
// sustained loss is retained as an explicit timeline gap rather than filled
// with fabricated media data.
package rtp

import (
	"sort"

	"github.com/pion/rtp"

	"regress/internal/media"
)

// Gap is a contiguous run of sequence numbers never observed on the wire.
type Gap struct {
	FirstSeq uint16 // first missing sequence number
	LastSeq  uint16 // last missing sequence number (inclusive)
	Count    int    // number of missing packets
}

// TrackState is the recovered state of one RTP track/SSRC.
type TrackState struct {
	SSRC            uint32
	ClockRate       uint32
	Ordered         []*rtp.Packet // deduplicated, sequence-ordered packets
	Gaps            []Gap         // retained timeline gaps
	Duplicates      int
	Reordered       int
	BurstLost       int // packets lost in the largest contiguous burst
	FirstSeq        uint16
	LastSeq         uint16
	PayloadDigest   string
	ContainerDigest string
}

// WindowSize bounds how far out of order a packet may arrive before the
// reorder buffer gives up and commits a gap. Small by design: latency and
// memory stay bounded on long streams.
const WindowSize = 64

// receiver tracks per-SSRC recovery state.
type receiver struct {
	state    TrackState
	seen     map[uint16]struct{}
	buffer   map[uint16]*rtp.Packet
	nextSeq  uint16 // commit cursor: next sequence number to emit/gap
	highSeq  uint16 // highest sequence number ever observed
	haveBase bool
	gapStart uint16
	gapLen   int
	pay      *media.Digester
	cont     *media.Digester
}

// Recover processes a stream of already-unmarshalled RTP packets in arrival
// order and returns one TrackState per observed SSRC. Malformed entries are
// reported via the returned error list using the shared structured model.
func Recover(packets []*rtp.Packet, clockRates map[uint32]uint32) ([]*TrackState, []error) {
	recv := map[uint32]*receiver{}
	var order []uint32
	var errs []error
	for i, p := range packets {
		if p == nil {
			errs = append(errs, media.InvalidInput("rtp packet %d is nil", i))
			continue
		}
		r, ok := recv[p.SSRC]
		if !ok {
			r = &receiver{
				state:  TrackState{SSRC: p.SSRC, ClockRate: clockRates[p.SSRC]},
				seen:   map[uint16]struct{}{},
				buffer: map[uint16]*rtp.Packet{},
				pay:    media.NewDigester(media.KindPayload),
				cont:   media.NewDigester(media.KindContainer),
			}
			recv[p.SSRC] = r
			order = append(order, p.SSRC)
		}
		r.cont.WriteString("rtp\n")
		r.handle(p)
	}
	out := make([]*TrackState, 0, len(order))
	for _, ssrc := range order {
		out = append(out, recv[ssrc].finish())
	}
	return out, errs
}

func (r *receiver) handle(p *rtp.Packet) {
	seq := p.SequenceNumber
	if _, dup := r.seen[seq]; dup {
		r.state.Duplicates++
		return
	}
	r.seen[seq] = struct{}{}
	if !r.haveBase {
		r.haveBase = true
		r.nextSeq, r.highSeq = seq, seq
		r.state.FirstSeq = seq
	} else {
		// Reordering means a packet arrives below the highest sequence number
		// seen so far (its predecessors were still missing on arrival).
		if seqDistance(r.highSeq, seq) < 0 {
			r.state.Reordered++
		}
		// Advance the high-water mark when this packet extends it.
		if seqDistance(r.highSeq, seq) > 0 {
			r.highSeq = seq
		}
	}
	r.buffer[seq] = p
	r.pump()
}

// pump emits the in-order prefix and commits gaps once the high-water mark
// has moved more than WindowSize ahead of the commit cursor.
func (r *receiver) pump() {
	for {
		if p, ok := r.buffer[r.nextSeq]; ok {
			if r.gapLen > 0 {
				r.closeGap()
			}
			r.emit(p)
			delete(r.buffer, r.nextSeq)
			r.nextSeq++
			continue
		}
		// Cursor packet missing. If the high-water mark is still within the
		// reorder window, wait for a late arrival; otherwise commit the gap.
		if seqDistance(r.nextSeq, r.highSeq) <= WindowSize {
			return
		}
		if r.gapLen == 0 {
			r.gapStart = r.nextSeq
		}
		r.gapLen++
		r.nextSeq++
	}
}

func (r *receiver) closeGap() {
	g := Gap{
		FirstSeq: r.gapStart,
		LastSeq:  r.gapStart + uint16(r.gapLen-1),
		Count:    r.gapLen,
	}
	r.state.Gaps = append(r.state.Gaps, g)
	if r.gapLen > r.state.BurstLost {
		r.state.BurstLost = r.gapLen
	}
	r.gapLen = 0
}

func (r *receiver) emit(p *rtp.Packet) {
	r.state.Ordered = append(r.state.Ordered, p)
	r.state.LastSeq = p.SequenceNumber
	r.pay.Write(p.Payload)
	r.cont.WriteString("p")
}

func (r *receiver) finish() *TrackState {
	// Close the trailing gap (packets after the last observed one never
	// trigger pump again) and flush any buffered stragglers in order.
	for seqDistance(r.nextSeq, r.highSeq) > 0 {
		if _, ok := r.buffer[r.nextSeq]; ok {
			if r.gapLen > 0 {
				r.closeGap()
			}
			r.emit(r.buffer[r.nextSeq])
			delete(r.buffer, r.nextSeq)
			r.nextSeq++
			continue
		}
		if r.gapLen == 0 {
			r.gapStart = r.nextSeq
		}
		r.gapLen++
		r.nextSeq++
	}
	if r.gapLen > 0 {
		r.closeGap()
	}
	if len(r.buffer) != 0 { // defensive: nothing should remain
		seqs := make([]int, 0, len(r.buffer))
		for s := range r.buffer {
			seqs = append(seqs, int(s))
		}
		sort.Ints(seqs)
		for _, s := range seqs {
			r.emit(r.buffer[uint16(s)])
		}
	}
	r.state.PayloadDigest = r.pay.Sum()
	r.state.ContainerDigest = r.cont.Sum()
	return &r.state
}

// seqDistance returns the signed forward distance from a to b in uint16
// sequence space (positive when b is ahead of a).
func seqDistance(a, b uint16) int {
	return int(int16(b - a))
}

// MediaTime converts an RTP timestamp at the track clock rate to unified
// media time. A zero clock rate is rejected rather than silently treated as 1.
func MediaTime(rtpTimestamp uint32, clockRate uint32) (media.Rat, error) {
	if clockRate == 0 {
		return media.Rat{}, media.InvalidInput("RTP clock rate must be non-zero")
	}
	return media.MediaTime(int64(rtpTimestamp), int64(clockRate))
}
