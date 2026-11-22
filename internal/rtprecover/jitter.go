// Package rtprecover reorders, de-duplicates and gap-tracks RTP packets
// within a bounded reorder window. Contiguous packets are emitted
// immediately for low latency; a missing sequence is given window arrivals
// to show up late, after which the gap is finalized as an unrecoverable
// loss through the shared clock.Error taxonomy instead of being dropped
// silently.
package rtprecover

import (
	"sync"

	"github.com/pion/rtp"

	"mediasync/internal/clock"
)

// MaxRecoveryWindow caps how far out of order a packet may arrive and still
// be accepted. A larger window tolerates worse jitter at the cost of latency
// and memory; inputs demanding more are rejected up front.
const MaxRecoveryWindow = 4096

// Stats is the recovery accounting consumed by the sync report.
type Stats struct {
	Expected      int // sequence positions in the observed span
	Received      int // distinct packets accepted
	Duplicates    int // repeated deliveries of an already-known seq
	Reordered     int // accepted packets arriving below the high-water mark
	Recovered     int // gaps filled by a late packet before finalization
	Unrecoverable int // gaps finalized while still open
}

// RecoveryRate is the fraction of the observed sequence span that was
// delivered. Truly dropped packets cannot be reconstructed without
// retransmission, so this is the honest end-to-end delivery figure.
func (s Stats) RecoveryRate() float64 {
	if s.Expected == 0 {
		return 1
	}
	return float64(s.Expected-s.Unrecoverable) / float64(s.Expected)
}

// Recoverer is one SSRC's bounded jitter buffer. It is safe for concurrent
// Push calls; emission order stays deterministic by sequence number.
type Recoverer struct {
	mu     sync.Mutex
	track  uint32
	window int
	tl     *clock.Timeline

	buffered map[uint16]*rtp.Packet
	started  bool
	origin   uint16 // first sequence ever accepted, anchors Expected
	emit     uint16 // next sequence to emit (contiguous drain frontier)
	aged     uint16 // positions below this have left the recovery window
	high     uint16 // highest sequence accepted so far
	stats    Stats
}

// NewRecoverer binds an SSRC-equivalent track id, its reorder window and the
// shared timeline used to reduce RTP timestamps onto the nanosecond base.
func NewRecoverer(track uint32, window int, tl *clock.Timeline) (*Recoverer, error) {
	if window <= 0 || window > MaxRecoveryWindow {
		return nil, &clock.Error{Kind: clock.KindScale, Track: track,
			Msg: "recovery window out of bounds"}
	}
	return &Recoverer{
		track:    track,
		window:   window,
		tl:       tl,
		buffered: make(map[uint16]*rtp.Packet),
	}, nil
}

// seqDistance returns the forward modular distance from a to b in uint16
// space; differences within +-32767 are interpreted without ambiguity.
func seqDistance(a, b uint16) int { return int(int16(b - a)) }

// Delivered pairs a finalized packet with its shared-base nanosecond time.
type Delivered struct {
	Packet *rtp.Packet
	NS     int64
}

// Push accepts one packet and returns every packet finalized by this call in
// strict sequence order. A gap that ages past the reorder window finalizes
// as an unrecoverable loss and is returned as clock.KindPacketLoss alongside
// any packets finalized at the same time, so the caller can propagate it into
// the sync statistics without halting the stream.
func (r *Recoverer) Push(pkt *rtp.Packet) ([]Delivered, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.started {
		r.started = true
		// Every frontier starts at the first accepted sequence. It can only
		// age out after window+1 *further* arrivals, never on the first push.
		r.origin, r.emit, r.aged, r.high = pkt.SequenceNumber,
			pkt.SequenceNumber, pkt.SequenceNumber, pkt.SequenceNumber
		r.buffered[pkt.SequenceNumber] = pkt
		r.stats.Received++
		r.stats.Expected = 1
		return r.drainContiguous(), nil
	}

	// Strictly behind the emit frontier has already been finalized; the
	// frontier seq itself is the expected next packet, not a duplicate. A
	// second copy of a buffered seq is a duplicate too.
	if seqDistance(r.emit, pkt.SequenceNumber) < 0 || r.buffered[pkt.SequenceNumber] != nil {
		r.stats.Duplicates++
		return nil, nil
	}

	if seqDistance(r.high, pkt.SequenceNumber) > 0 {
		r.high = pkt.SequenceNumber
	} else {
		// Inside the window but below the high-water mark: a late arrival
		// closing an open gap.
		r.stats.Reordered++
		r.stats.Recovered++
	}
	r.buffered[pkt.SequenceNumber] = pkt
	r.stats.Received++
	r.stats.Expected = seqDistance(r.origin, r.high) + 1

	lossErr := r.ageWindow(pkt.SequenceNumber)
	emitted := r.drainContiguous()
	return emitted, lossErr
}

// ageWindow finalizes positions that have stayed open for more than window
// arrivals. The newest sequence is the reference: the gap at the aged
// frontier dies once the frontier-to-latest distance exceeds the window, so
// a packet may arrive exactly window steps late and still be accepted.
func (r *Recoverer) ageWindow(latest uint16) error {
	var lossErr error
	for seqDistance(r.aged, latest) > r.window {
		seq := r.aged
		if r.buffered[seq] == nil {
			r.stats.Unrecoverable++
			if lossErr == nil {
				lossErr = &clock.Error{Kind: clock.KindPacketLoss, Track: r.track,
					Seq: uint64(seq), Msg: "packet gap exceeded recovery window"}
			}
		}
		delete(r.buffered, seq)
		r.aged++
		// Keep the emit frontier at or behind aged; buffered contiguous
		// packets past the aged point remain eligible for immediate drain.
		if seqDistance(r.emit, r.aged) > 0 {
			r.emit = r.aged
		}
	}
	return lossErr
}

// drainContiguous emits buffered packets from the emit frontier while the
// next expected sequence is present, reducing timestamps through the
// timeline. The aged frontier tracks the emit frontier: delivered positions
// can never later be mistaken for aged-out losses.
func (r *Recoverer) drainContiguous() []Delivered {
	var out []Delivered
	for {
		pkt, ok := r.buffered[r.emit]
		if !ok {
			return out
		}
		delete(r.buffered, r.emit)
		r.emit++
		r.aged = r.emit
		ns, err := r.tl.Reduce(r.track, uint64(pkt.SequenceNumber), int64(pkt.Timestamp))
		if err != nil {
			// Clock failures (monotonicity/wrap) propagate to the caller's
			// statistics but cannot corrupt the frontier.
			continue
		}
		out = append(out, Delivered{Packet: pkt, NS: ns})
	}
}

// Flush ends the stream: every open position up to the high-water mark is
// finalized. Missing positions are confirmed unrecoverable (they had the
// whole stream to arrive and never did) and reported as clock.KindPacketLoss,
// while buffered packets still emit in strict sequence order.
func (r *Recoverer) Flush() ([]Delivered, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started {
		return nil, nil
	}
	var lossErr error
	// Positions below aged already crossed the window during Push and were
	// accounted there; only still-open positions inside the window are new
	// end-of-stream losses.
	if seqDistance(r.aged, r.emit) > 0 {
		r.emit = r.aged
	}
	for seqDistance(r.emit, r.high) > 0 {
		seq := r.emit
		if r.buffered[seq] == nil {
			r.stats.Unrecoverable++
			if lossErr == nil {
				lossErr = &clock.Error{Kind: clock.KindPacketLoss, Track: r.track,
					Seq: uint64(seq), Msg: "packet gap still open at end of stream"}
			}
		}
		r.emit++
	}
	return r.drainContiguous(), lossErr
}

// Stats returns a copy of the recovery accounting.
func (r *Recoverer) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats
}
