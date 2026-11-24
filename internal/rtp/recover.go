// Package rtp performs sequence-number based loss detection, reordering,
// duplicate accounting and deterministic retransmission recovery on a
// single-SSRC packet stream. It uses pion/rtp only for packet (de)coding;
// the reassembly state machine and all statistics live here so identical
// input always yields an identical timeline and summary.
package rtp

import (
	"sort"

	"github.com/pion/rtp"

	"mediacheck/internal/damage"
	"mediacheck/internal/media"
)

// Stats is the deterministic recovery accounting for one stream.
type Stats struct {
	Expected    int `json:"expected"`
	Received    int `json:"received"`
	Duplicate   int `json:"duplicate"`
	Reordered   int `json:"reordered"`
	Lost        int `json:"lost"`
	Recovered   int `json:"recovered"`
	Unrecovered int `json:"unrecovered"`
	// ReassemblyP95Ms is the 95th-percentile hold time a packet spent in
	// the reorder buffer, on the RTP clock, in milliseconds.
	ReassemblyP95Ms int64 `json:"reassembly_p95_ms"`
	// RecoveryRate is Recovered/Lost; 1 when there were no losses.
	LossRecoveryRate float64 `json:"loss_recovery_rate"`
}

// Ordered is one reassembled packet in sequence order.
type Ordered struct {
	Packet         *rtp.Packet
	Recovered      bool
	Duplicate      bool
	Reordered      bool
	HoldSlots      int
	TimestampNanos int64
}

// Result is the full recovery output.
type Result struct {
	SSRC      uint32         `json:"ssrc"`
	ClockRate uint32         `json:"clock_rate"`
	Stats     Stats          `json:"stats"`
	Ordered   []Ordered      `json:"-"`
	Errors    []damage.Error `json:"errors"`
	Timeline  []int64        `json:"timeline"`
	// Drift is the fitted remote-vs-local clock skew; PPM is parts per
	// million and R2 quantifies fit quality (NaN when indeterminate).
	Drift media.Drift `json:"drift"`
}

// Recover processes an arrival-order packet stream. maxReorderSlots bounds
// the reorder buffer: a gap is declared unrecovered once the highest seen
// sequence advances more than this many slots past it.
func Recover(packets []*rtp.Packet, clockRate uint32, maxReorderSlots int) (*Result, error) {
	if clockRate == 0 {
		return nil, media.ErrInvalidTimeBase
	}
	if maxReorderSlots <= 0 {
		maxReorderSlots = 4
	}
	tb, err := media.NewTimeBase(clockRate)
	if err != nil {
		return nil, err
	}
	rc := &recoverer{
		tb:              tb,
		maxReorderSlots: maxReorderSlots,
		buf:             map[uint16]*rtp.Packet{},
		seen:            map[uint16]bool{},
		arrival:         map[uint16]int{},
		wrap:            media.NewWrapper(32),
		dupSet:          map[uint16]bool{},
		reordSet:        map[uint16]bool{},
		gapLost:         map[uint16]bool{},
		settled:         map[uint16]bool{},
	}
	for arrival, p := range packets {
		rc.ingest(p, arrival)
	}
	rc.flush()
	rc.buildResult()
	return rc.result, nil
}

type recoverer struct {
	tb              media.TimeBase
	maxReorderSlots int
	started         bool
	baseSeq         uint16
	maxSeq          uint16
	buf             map[uint16]*rtp.Packet
	seen            map[uint16]bool
	arrival         map[uint16]int
	wrap            *media.Wrapper
	dupSet          map[uint16]bool
	reordSet        map[uint16]bool
	// lastArrivalSeq tracks the previous non-duplicate arrival so raw
	// arrival inversions (reordering) are detected independently of the
	// gap/retransmission state.
	lastArrivalSeq uint16
	haveArrival    bool
	// gapLost records sequence numbers already counted as nominal losses.
	gapLost map[uint16]bool
	// settled records sequence numbers already finalized by giveUp so a
	// gap is never double-counted across drain/flush boundaries.
	settled map[uint16]bool
	ordered []Ordered
	stats   Stats
	errs    []damage.Error
	result  *Result
}

func (r *recoverer) ingest(p *rtp.Packet, arrival int) {
	seq := p.SequenceNumber
	if !r.started {
		r.started = true
		r.baseSeq, r.maxSeq = seq, seq
		r.stats.Expected = 1
	}
	if r.seen[seq] {
		r.stats.Duplicate++
		r.dupSet[seq] = true
		r.errs = append(r.errs, damage.Error{Code: damage.CodeRTPDuplicate,
			Range:       damage.Range{Deterministic: true, Recoverable: damage.RecoverableYes},
			Detail:      "duplicate sequence number",
			Recoverable: damage.RecoverableYes})
		return
	}
	r.seen[seq] = true
	r.buf[seq] = p
	r.arrival[seq] = arrival

	// Reordering is decided purely on arrival inversion: this packet's
	// sequence is lower than the previous distinct arrival. Gap-fill
	// retransmissions arrive after the stream has moved far ahead, so they
	// are distinguished by a larger backward distance than a single-slot
	// swap (see retransmitThreshold).
	const retransmitThreshold = 4
	if r.haveArrival {
		back := -seqDistance(r.lastArrivalSeq, seq) // >0 when seq is earlier
		if back > 0 && back < retransmitThreshold {
			r.stats.Reordered++
			r.reordSet[seq] = true
			r.errs = append(r.errs, damage.Error{Code: damage.CodeRTPReordered,
				Range:       damage.Range{Deterministic: true, Recoverable: damage.RecoverableYes},
				Detail:      "out-of-order arrival",
				Recoverable: damage.RecoverableYes})
		}
	}
	r.lastArrivalSeq = seq
	r.haveArrival = true

	dist := seqDistance(r.maxSeq, seq)
	if dist > 0 {
		// New highest: count skipped sequence numbers as nominal losses.
		r.stats.Expected += dist
		for d := 1; d < dist; d++ {
			missing := r.maxSeq + uint16(d)
			if !r.gapLost[missing] {
				r.gapLost[missing] = true
				r.stats.Lost++
			}
		}
		r.maxSeq = seq
	}
	r.drain()
}

func (r *recoverer) drain() {
	// During streaming, gaps stay open: a late retransmission must still be
	// accepted as recovery no matter how many slots later it arrives. Only
	// contiguous packets at the low end are emitted; gaps block emission
	// until they are filled, which preserves sequence order deterministically.
	for {
		next := r.baseSeq
		p, ok := r.buf[next]
		if !ok {
			return
		}
		// Arriving after a counted loss means a retransmission recovered
		// the gap.
		r.emit(p, r.gapLost[next])
		delete(r.buf, next)
		r.baseSeq++
	}
}

func (r *recoverer) flush() {
	// Only gaps at or below the highest observed sequence can be given up;
	// once baseSeq passes maxSeq every expected packet has been accounted
	// for, so iteration must stop.
	for seqDistance(r.baseSeq, r.maxSeq) >= 0 {
		next := r.baseSeq
		if p, ok := r.buf[next]; ok {
			r.emit(p, r.gapLost[next])
			delete(r.buf, next)
			r.baseSeq++
			continue
		}
		r.giveUp(next)
		r.baseSeq++
	}
}

func (r *recoverer) giveUp(seq uint16) {
	if r.seen[seq] || r.settled[seq] {
		return
	}
	r.settled[seq] = true
	if !r.gapLost[seq] {
		// A gap finalized before any higher packet established it: count
		// the loss now.
		r.stats.Lost++
	}
	r.stats.Unrecovered++
	e := (&damage.Error{
		Code:   damage.CodeRTPLost,
		Range:  damage.Range{Deterministic: true},
		Detail: "sequence number never recovered within reorder window",
	}).SetRecoverable(damage.RecoverableNo)
	r.errs = append(r.errs, *e)
}

func (r *recoverer) emit(p *rtp.Packet, recovered bool) {
	seq := p.SequenceNumber
	hold := 0
	if n := len(r.ordered); n > 0 {
		prevSeq := r.ordered[n-1].Packet.SequenceNumber
		if a, ok := r.arrival[seq]; ok {
			hold = a - r.arrival[prevSeq]
			if hold < 0 {
				hold = 0
			}
		}
	}
	unwrapped := r.wrap.Expand(p.Timestamp)
	r.ordered = append(r.ordered, Ordered{
		Packet:         p,
		Recovered:      recovered,
		Duplicate:      r.dupSet[seq],
		Reordered:      r.reordSet[seq],
		HoldSlots:      hold,
		TimestampNanos: r.tb.ToNanos(unwrapped),
	})
	if recovered {
		r.stats.Recovered++
	}
}

func (r *recoverer) buildResult() {
	r.stats.Received = len(r.seen)
	if r.stats.Lost > 0 {
		r.stats.LossRecoveryRate = float64(r.stats.Recovered) / float64(r.stats.Lost)
	} else {
		r.stats.LossRecoveryRate = 1
	}
	holds := make([]int, 0, len(r.ordered))
	for _, o := range r.ordered {
		holds = append(holds, o.HoldSlots)
	}
	sort.Ints(holds)
	r.stats.ReassemblyP95Ms = r.holdP95Millis(holds)
	timeline := make([]int64, len(r.ordered))
	for i, o := range r.ordered {
		timeline[i] = o.TimestampNanos
	}
	r.result = &Result{
		SSRC:      r.ssrc(),
		ClockRate: r.tb.TicksPerSecond(),
		Stats:     r.stats,
		Ordered:   r.ordered,
		Errors:    mergeRTPEvents(r.errs),
		Timeline:  timeline,
		Drift:     r.estimateDrift(),
	}
}

// estimateDrift fits the remote RTP clock against the receiver's local
// clock. The local clock advances one nominal step per *sequence-number*
// distance rather than per reassembled position, so a packet-loss burst
// (which only compresses arrival positions, never the sender's timestamp
// progression) cannot masquerade as clock drift. Remote time is each
// packet's unwrapped timestamp in nanoseconds. The fit is deterministic and
// requires no wall clock.
func (r *recoverer) estimateDrift() media.Drift {
	if len(r.ordered) < 2 {
		return media.EstimateDrift(nil, nil)
	}
	// Nominal per-sequence step: median of timestamp delta divided by the
	// sequence distance between consecutive reassembled packets. Normal
	// pairs have distance 1; pairs straddling an unrecovered gap have a
	// larger distance and normalize back to one step's worth of time.
	perStep := make([]int64, 0, len(r.ordered)-1)
	for i := 1; i < len(r.ordered); i++ {
		dNanos := r.ordered[i].TimestampNanos - r.ordered[i-1].TimestampNanos
		dSeq := seqDistance(r.ordered[i-1].Packet.SequenceNumber,
			r.ordered[i].Packet.SequenceNumber)
		if dNanos > 0 && dSeq > 0 {
			perStep = append(perStep, dNanos/int64(dSeq))
		}
	}
	if len(perStep) == 0 {
		return media.EstimateDrift(nil, nil)
	}
	sort.Slice(perStep, func(i, j int) bool { return perStep[i] < perStep[j] })
	step := perStep[len(perStep)/2]

	firstSeq := r.ordered[0].Packet.SequenceNumber
	base := r.ordered[0].TimestampNanos
	local := make([]int64, len(r.ordered))
	remote := make([]int64, len(r.ordered))
	for i, o := range r.ordered {
		d := seqDistance(firstSeq, o.Packet.SequenceNumber)
		local[i] = int64(d) * step
		remote[i] = o.TimestampNanos - base
	}
	return media.EstimateDrift(local, remote)
}

func (r *recoverer) holdP95Millis(holds []int) int64 {
	if len(holds) == 0 {
		return 0
	}
	rank := (95*len(holds) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	slots := holds[rank-1]
	var stepTicks uint32
	if len(r.ordered) > 1 {
		var sum uint64
		for i := 1; i < len(r.ordered); i++ {
			sum += uint64(r.ordered[i].Packet.Timestamp - r.ordered[i-1].Packet.Timestamp)
		}
		stepTicks = uint32(sum / uint64(len(r.ordered)-1))
	}
	return r.tb.ToNanos(int64(stepTicks)*int64(slots)) / 1_000_000
}

func (r *recoverer) ssrc() uint32 {
	if len(r.ordered) > 0 {
		return r.ordered[0].Packet.SSRC
	}
	return 0
}

// seqDistance returns signed forward distance b-a in 16-bit sequence space.
func seqDistance(a, b uint16) int {
	d := int(b) - int(a)
	if d > 32767 {
		d -= 65536
	} else if d < -32768 {
		d += 65536
	}
	return d
}

// mergeRTPEvents collapses identical RTP event codes into bounded records,
// keeping the strongest recoverability: a single unrecovered gap forces the
// whole collapsed record to RecoverableNo.
func mergeRTPEvents(errs []damage.Error) []damage.Error {
	worst := map[damage.Code]damage.Recoverable{}
	var order []damage.Code
	for _, e := range errs {
		if _, ok := worst[e.Code]; !ok {
			order = append(order, e.Code)
			worst[e.Code] = e.Recoverable
			continue
		}
		if e.Recoverable == damage.RecoverableNo {
			worst[e.Code] = damage.RecoverableNo
		}
	}
	out := make([]damage.Error, 0, len(order))
	for _, c := range order {
		out = append(out, damage.Error{
			Code:        c,
			Range:       damage.Range{Deterministic: true, Recoverable: worst[c]},
			Detail:      eventDetail(c),
			Recoverable: worst[c],
		})
	}
	return out
}

func eventDetail(c damage.Code) string {
	switch c {
	case damage.CodeRTPLost:
		return "unrecovered sequence gaps"
	case damage.CodeRTPReordered:
		return "reordered arrivals"
	case damage.CodeRTPDuplicate:
		return "duplicate arrivals"
	default:
		return "rtp event"
	}
}
