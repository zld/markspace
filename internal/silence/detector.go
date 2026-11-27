// Package silence turns a stream of RTP audio packets into silence and
// unknown intervals on the canonical nanosecond time base. Packets use
// 16-bit big-endian signed PCM payloads.
//
// Ordering guarantees:
//   - duplicate RTP sequence numbers are counted but never timed twice;
//   - out-of-order packets are reordered inside a bounded sequence window;
//   - when the window is exhausted while sequence numbers are still missing,
//     the missing span is reported as unknown rather than guessed silent.
//
// All timestamp conversion happens in wire (delivery) order, so out-of-order
// arrivals never make the unwrapped RTP clock go backwards.
package silence

import (
	"encoding/binary"
	"errors"
	"math"

	"github.com/pion/rtp"

	"example.com/continuity/internal/clock"
	"example.com/continuity/internal/fault"
)

// IntervalKind classifies a reported audio interval.
type IntervalKind uint8

const (
	// KindSilence spans packets whose PCM samples are all within threshold.
	KindSilence IntervalKind = iota + 1
	// KindUnknown spans sequence numbers that never arrived in window.
	KindUnknown
)

// Interval is one contiguous classified audio span, half-open in nanos.
type Interval struct {
	StartNS int64
	EndNS   int64
	Kind    IntervalKind
}

// Config configures the detector.
type Config struct {
	Track            string
	ClockRate        int64 // RTP timestamp ticks per second, e.g. 48000
	SamplesPerPacket int   // PCM samples carried by every packet
	SilenceThreshold int16 // max |sample| considered silent
	ReorderWindow    int   // max packet distance held while waiting for gaps
}

// Report is the detector result.
type Report struct {
	Intervals        []Interval
	SilenceNS        int64
	UnknownNS        int64
	OrderedPackets   int
	DuplicatePackets int
	UnknownRanges    int
}

type buffered struct {
	pkt    *rtp.Packet
	offset int64
}

type gap struct {
	from  uint16 // first missing seq (inclusive)
	until uint16 // anchor seq that proved the gap (exclusive)
}

// Detector is a stateful reordering silence detector.
type Detector struct {
	cfg      Config
	duration int64 // packet duration in nanoseconds
	buf      map[uint16]buffered
	seen     map[uint16]struct{} // sequence numbers already delivered
	gaps     []gap
	expected uint16 // delivery cursor; only advances in wire order
	first    uint16 // sequence number of the stream anchor
	highest  uint16 // largest sequence number observed so far
	have     bool
	report   Report
	// RTP timestamp conversion state, updated on delivered packets only.
	anchorTS  uint32
	anchorNS  int64
	anchorSeq uint16
	haveTS    bool
	// Open silence range.
	silOpen  bool
	silStart int64
	silEnd   int64
}

// New validates the config and builds a detector.
func New(cfg Config) (*Detector, error) {
	if cfg.ClockRate <= 0 {
		return nil, fault.New(fault.StageSilence, "config", clock.ErrInvalidTick)
	}
	if cfg.SamplesPerPacket <= 0 {
		return nil, fault.New(fault.StageSilence, "config",
			errors.New("samples per packet must be positive"))
	}
	if cfg.ReorderWindow <= 0 {
		return nil, fault.New(fault.StageSilence, "config",
			errors.New("reorder window must be positive"))
	}
	dur, err := clock.ToNanos(int64(cfg.SamplesPerPacket), cfg.ClockRate)
	if err != nil {
		return nil, fault.New(fault.StageSilence, "config", err)
	}
	return &Detector{
		cfg: cfg, duration: dur,
		buf:  map[uint16]buffered{},
		seen: map[uint16]struct{}{},
	}, nil
}

// seqDist returns signed modular distance from ref to seq.
func seqDist(ref, seq uint16) int {
	return int(int16(seq - ref))
}

// Push accepts one RTP packet in arrival order.
func (d *Detector) Push(pkt *rtp.Packet, offset int64) error {
	seq := pkt.SequenceNumber
	if !d.have {
		// The first observed sequence number anchors the stream; earlier
		// numbers belong to a previous generation and are not "missing".
		d.expected, d.first, d.highest, d.have = seq, seq, seq, true
	} else {
		// Outside the [first, highest] generation on the low side means a
		// packet from before the stream anchor: count but never time it.
		if seqDist(d.first, seq) < 0 {
			d.report.DuplicatePackets++
			return nil
		}
		if d.inKnownGap(seq) {
			// A seq we already declared unknown: never timed.
			d.report.DuplicatePackets++
			return nil
		}
	}
	if _, dup := d.seen[seq]; dup {
		// Already delivered in order: a genuine retransmission/duplicate.
		d.report.DuplicatePackets++
		return nil
	}
	if _, exists := d.buf[seq]; exists {
		d.report.DuplicatePackets++
		return nil
	}
	if d.have && seqDist(d.highest, seq) > 0 {
		d.highest = seq
	}
	// A forward gap beyond the reorder window means the missing sequence
	// numbers will never become reorderable. The gap is permanent; the
	// anchor packet itself remains buffered for normal delivery.
	if dist := seqDist(d.expected, seq); dist > d.cfg.ReorderWindow {
		d.gaps = append(d.gaps, gap{from: d.expected, until: seq})
	}
	d.buf[seq] = buffered{pkt, offset}
	return d.drain()
}

// inKnownGap reports whether seq falls inside a window-exhausted gap.
func (d *Detector) inKnownGap(seq uint16) bool {
	for _, g := range d.gaps {
		if d := seqDist(g.from, seq); d >= 0 && d < seqDist(g.from, g.until) {
			return true
		}
	}
	return false
}

func mulDuration(dur, n int64) (int64, error) {
	if n <= 0 || dur <= 0 {
		return 0, errors.New("non-positive span")
	}
	if dur > math.MaxInt64/n {
		return 0, clock.ErrOverflow
	}
	return dur * n, nil
}

// drain delivers every buffered packet contiguous from the delivery cursor.
// The cursor stops at the smallest not-yet-delivered sequence number (a gap
// that reordering might still fill) so later out-of-order arrivals are not
// mistaken for duplicates. Permanently-unknown gap positions are skipped.
func (d *Detector) drain() error {
	for {
		// Permanently unknown gap position: wait for its anchor packet,
		// resolve the gap nanos, then jump the cursor to the anchor so it is
		// delivered by a normal iteration (never silently skipped).
		if d.inKnownGap(d.expected) {
			anchorSeq := d.anchorOfGap(d.expected)
			anchor, ok := d.buf[anchorSeq]
			if !ok {
				return nil
			}
			if err := d.resolveGap(anchorSeq, anchor); err != nil {
				return err
			}
			d.expected = anchorSeq
			continue
		}
		b, ok := d.buf[d.expected]
		if !ok {
			return nil
		}
		if err := d.deliver(b); err != nil {
			return err
		}
		d.seen[d.expected] = struct{}{}
		delete(d.buf, d.expected)
		d.expected++
	}
}

// anchorOfGap returns the anchor sequence number of the gap covering seq.
func (d *Detector) anchorOfGap(seq uint16) uint16 {
	for _, g := range d.gaps {
		if dist := seqDist(g.from, seq); dist >= 0 && dist < seqDist(g.from, g.until) {
			return g.until
		}
	}
	return seq
}

// resolveGap converts one permanent gap ending at anchor into an unknown
// interval on the canonical time base.
func (d *Detector) resolveGap(anchorSeq uint16, anchor buffered) error {
	kept := d.gaps[:0]
	for _, g := range d.gaps {
		if g.until != anchorSeq {
			kept = append(kept, g)
			continue
		}
		missing := int64(seqDist(g.from, g.until))
		if missing <= 0 {
			continue
		}
		anchorStart, err := d.packetStartNS(anchor.pkt)
		if err != nil {
			return d.fail(anchor.pkt, anchor.offset, "timestamp", err)
		}
		span, err := mulDuration(d.duration, missing)
		if err != nil {
			return d.fail(anchor.pkt, anchor.offset, "unknown-span", err)
		}
		start, err := clock.Sub(anchorStart, span)
		if err != nil {
			return d.fail(anchor.pkt, anchor.offset, "unknown-start", err)
		}
		d.closeSilence()
		d.appendInterval(Interval{StartNS: start, EndNS: anchorStart, Kind: KindUnknown})
		d.report.UnknownRanges++
	}
	d.gaps = kept
	return nil
}

func (d *Detector) deliver(b buffered) error {
	pkt := b.pkt
	start, err := d.packetStartNS(pkt)
	if err != nil {
		return d.fail(pkt, b.offset, "timestamp", err)
	}
	end, err := clock.Add(start, d.duration)
	if err != nil {
		return d.fail(pkt, b.offset, "duration", err)
	}
	silent, err := d.isSilent(pkt, b.offset)
	if err != nil {
		return err
	}
	d.report.OrderedPackets++
	if silent {
		if !d.silOpen {
			d.silOpen, d.silStart, d.silEnd = true, start, end
		} else {
			d.silEnd = end
		}
		return nil
	}
	d.closeSilence()
	return nil
}

func (d *Detector) closeSilence() {
	if !d.silOpen {
		return
	}
	d.appendInterval(Interval{StartNS: d.silStart, EndNS: d.silEnd, Kind: KindSilence})
	d.silOpen = false
}

func (d *Detector) appendInterval(iv Interval) {
	d.report.Intervals = append(d.report.Intervals, iv)
	switch iv.Kind {
	case KindSilence:
		d.report.SilenceNS += iv.EndNS - iv.StartNS
	case KindUnknown:
		d.report.UnknownNS += iv.EndNS - iv.StartNS
	}
}

// packetStartNS converts an RTP packet to its canonical start time. The
// first delivered packet anchors both the sequence number and the timestamp;
// every later packet is placed by its wire sequence distance, so the result
// is independent of arrival order and never goes backwards.
func (d *Detector) packetStartNS(pkt *rtp.Packet) (int64, error) {
	if !d.haveTS {
		ns, err := clock.ToNanos(int64(pkt.Timestamp), d.cfg.ClockRate)
		if err != nil {
			return 0, err
		}
		d.anchorTS, d.anchorNS, d.anchorSeq, d.haveTS = pkt.Timestamp, ns, pkt.SequenceNumber, true
		return ns, nil
	}
	// int16 conversion performs the modular RTP sequence wraparound.
	seqDelta := int64(int16(pkt.SequenceNumber - d.anchorSeq))
	if seqDelta >= 0 {
		offsetNS, err := mulDuration(d.duration, seqDelta)
		if err != nil {
			return 0, err
		}
		return clock.Add(d.anchorNS, offsetNS)
	}
	offsetNS, err := mulDuration(d.duration, -seqDelta)
	if err != nil {
		return 0, err
	}
	return clock.Sub(d.anchorNS, offsetNS)
}

func (d *Detector) isSilent(pkt *rtp.Packet, offset int64) (bool, error) {
	if len(pkt.Payload)%2 != 0 {
		return false, d.fail(pkt, offset, "pcm", fault.ErrLength)
	}
	for i := 0; i < len(pkt.Payload); i += 2 {
		v := int16(binary.BigEndian.Uint16(pkt.Payload[i:]))
		if v < 0 {
			v = -v
		}
		if v > d.cfg.SilenceThreshold {
			return false, nil
		}
	}
	return true, nil
}

func (d *Detector) fail(pkt *rtp.Packet, offset int64, op string, cause error) *fault.Error {
	e := fault.New(fault.StageSilence, op, cause).
		AtTrack(d.cfg.Track).AtFrame(int64(pkt.SequenceNumber))
	if offset >= 0 {
		e.AtOffset(offset)
	}
	return e
}

// Finish flushes buffered packets; any unresolved trailing gap is unknown.
func (d *Detector) Finish() (*Report, error) {
	for len(d.buf) > 0 {
		if d.inKnownGap(d.expected) {
			if err := d.drain(); err != nil {
				return nil, err
			}
			continue
		}
		if _, ok := d.buf[d.expected]; !ok {
			// Trailing hole: find the nearest buffered seq and declare the
			// span between cursor and it permanently unknown.
			var nearest uint16
			best := math.MaxInt
			for seq := range d.buf {
				if dist := seqDist(d.expected, seq); dist >= 0 && dist < best {
					best, nearest = dist, seq
				}
			}
			if best == math.MaxInt {
				break
			}
			d.gaps = append(d.gaps, gap{from: d.expected, until: nearest})
		}
		if err := d.drain(); err != nil {
			return nil, err
		}
	}
	d.closeSilence()
	r := d.report
	return &r, nil
}
