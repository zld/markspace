package rtprecover

import (
	"encoding/binary"
	"hash/crc32"

	"github.com/medialab/medialab/internal/clock"
	"github.com/pion/rtp"
)

// Envelope is the wire payload layout: 1 flag byte followed by body bytes and
// a big-endian CRC-32 (IEEE) covering flags+body. It lets the corrupt fixture
// produce deterministically checksum-bad packets without external capture.
type Envelope struct {
	Flags byte
	Body  []byte
}

// crcTable is the standard IEEE polynomial used by the self-generated payload
// envelopes; a package-level table avoids per-packet allocation.
var crcTable = crc32.MakeTable(crc32.IEEE)

// Marshal serializes the envelope as flags || body || CRC32(flags||body).
func (e Envelope) Marshal() []byte {
	out := make([]byte, 1+len(e.Body)+4)
	out[0] = e.Flags
	copy(out[1:], e.Body)
	sum := crc32.Checksum(out[:1+len(e.Body)], crcTable)
	binary.BigEndian.PutUint32(out[1+len(e.Body):], sum)
	return out
}

// ParseEnvelope verifies the trailing CRC-32 before trusting any byte inside.
func ParseEnvelope(payload []byte) (Envelope, error) {
	if len(payload) < 5 {
		return Envelope{}, clock.NewError(clock.KindTruncated, "rtprecover", "", 0,
			"payload shorter than envelope header")
	}
	bodyLen := len(payload) - 5
	got := binary.BigEndian.Uint32(payload[1+bodyLen:])
	want := crc32.Checksum(payload[:1+bodyLen], crcTable)
	if got != want {
		return Envelope{}, clock.NewError(clock.KindChecksum, "rtprecover", "", 0,
			"envelope CRC-32 mismatch")
	}
	body := make([]byte, bodyLen)
	copy(body, payload[1:1+bodyLen])
	return Envelope{Flags: payload[0], Body: body}, nil
}

// Gap is a confirmed missing sequence span with its timeline time range.
type Gap struct {
	TrackID  string
	StartSeq uint16
	EndSeq   uint16 // inclusive
	Ticks    clock.Range
	Length   int
}

// RecoverConfig parameterizes burst detection and reorder behavior.
type RecoverConfig struct {
	WindowCapacity int
	BurstThreshold int // consecutive missing seqs that constitute a reportable gap
	TrackID        string
	IsVideo        bool // video waits for a keyframe after a gap
	// TickOfPacket maps a full RTP packet to its manifest tick. It lets
	// callers derive time from the 32-bit RTP timestamp (which survives far
	// longer than the 16-bit sequence number) instead of a wrapping seq. When
	// nil, TickOfSeq is used.
	TickOfPacket func(*rtp.Packet) int64
	// TickOfSeq is the fallback mapping from sequence number to manifest tick.
	TickOfSeq func(uint16) int64
}

// Recover wraps a Window with checksum verification, burst-gap detection and
// keyframe-gated resumption. All buffering stays inside the bounded window.
type Recover struct {
	cfg          RecoverConfig
	win          *Window
	waitingKF    bool
	gaps         []Gap
	tickOfSeq    func(uint16) int64 // deterministic seq -> manifest tick mapping
	tickOfPkt    func(*rtp.Packet) int64
	lastTick     int64
	lastSeq      uint16 // sequence number of the most recently emitted sample
	maxObsSeq    uint16 // highest sequence number ever accepted
	haveLast     bool
	haveObsMax   bool
	earlySamples []Sample // emitted inside Push-time eviction, drained later
	stepTick     int64 // per-packet tick increment when timestamp mapping is used
}

// NewRecover builds a Recover. tickOf maps a sequence number to its manifest
// timeline tick so gap ranges carry real time positions. When cfg supplies
// TickOfPacket, that 32-bit-timestamp-based mapping takes precedence.
func NewRecover(cfg RecoverConfig, tickOf func(uint16) int64) *Recover {
	if cfg.BurstThreshold <= 0 {
		cfg.BurstThreshold = 1
	}
	cfg.TickOfSeq = tickOf
	r := &Recover{
		cfg:       cfg,
		win:       NewWindow(cfg.WindowCapacity),
		tickOfSeq: tickOf,
		tickOfPkt: cfg.TickOfPacket,
	}
	// Derive the per-packet tick increment from two adjacent seqs so gap
	// ranges stay linear even when sequence numbers wrap at 65535.
	r.stepTick = tickOf(1) - tickOf(0)
	return r
}

// tickOf resolves the manifest tick for a packet, preferring the 32-bit RTP
// timestamp mapping and falling back to the sequence-number mapping.
func (r *Recover) tickOf(pkt *rtp.Packet) int64 {
	if r.tickOfPkt != nil {
		return r.tickOfPkt(pkt)
	}
	return r.tickOfSeq(pkt.SequenceNumber)
}

// Push verifies and inserts one packet. Checksum-bad packets are counted but
// never enter the window, so they surface as real gaps instead of garbage.
func (r *Recover) Push(pkt *rtp.Packet) error {
	// Every observed seq moves the high-water mark, including rejected ones,
	// so end-of-stream can still see the tail gap they leave behind.
	r.noteObserved(pkt.SequenceNumber)
	if _, err := ParseEnvelope(pkt.Payload); err != nil {
		r.win.stats.ChecksumBad++
		return err
	}
	// A far-ahead packet with a hole parked at the head proves the missing
	// seqs are lost rather than reordered. Evict confirmed holes (subject to
	// the burst threshold) before the window can misclassify the packet as
	// late. This keeps a fast producer/slow consumer from corrupting counts.
	r.evictForIncoming(pkt.SequenceNumber)
	dropped, kind := r.win.Push(pkt)
	if dropped {
		if kind == DropDuplicate {
			return clock.NewError(clock.KindChecksum, "rtprecover", r.cfg.TrackID,
				int64(pkt.SequenceNumber), "duplicate sequence dropped")
		}
		return clock.NewError(clock.KindLate, "rtprecover", r.cfg.TrackID,
			int64(pkt.SequenceNumber), "packet outside reorder window")
	}
	// Keyframe-gated state is cleared when the keyframe becomes a sample
	// (makeSample), not when it merely enters the window, so eviction order
	// cannot release the wait before the delta frame is built.
	return nil
}

// evictForIncoming rotates over head holes that are both (a) confirmed by a
// buffered packet beyond them and (b) at least one burst-threshold long,
// recording a Gap for each evicted run. Contiguous packets at the head are
// flushed first so a parked head packet cannot mask the hole behind it.
func (r *Recover) evictForIncoming(incoming uint16) {
	head, ok := r.win.Head()
	if !ok {
		return
	}
	// A bounded queue under a fast producer can deliver many packets before
	// the consumer drains. Loop until the incoming seq actually fits: each
	// pass flushes contiguous head packets, then evicts the head hole.
	for seqDistance(head, incoming) >= int32(r.win.capacity) {
		if ferr := r.win.Flush(func(pkt *rtp.Packet) error {
			s, err := r.makeSample(pkt)
			if err != nil {
				return err
			}
			r.earlySamples = append(r.earlySamples, s)
			return nil
		}); ferr != nil {
			return
		}
		first := r.win.FirstFilled()
		if first <= 0 {
			return // nothing buffered beyond the head; cannot prove a hole
		}
		// Threshold-sized runs are always evicted as burst gaps. A smaller
		// head hole is evicted only when space pressure makes waiting
		// impossible: the incoming packet already exceeds window capacity.
		// Without this override a single checksum loss would wedge the head
		// and turn every later packet into a false late drop.
		belowThreshold := first < r.cfg.BurstThreshold
		if belowThreshold && !r.windowUnderPressure(incoming) {
			return
		}
		if r.win.EvictConfirmedHoles(first) != first {
			return
		}
		r.recordGap(head, first)
		if r.cfg.IsVideo {
			r.waitingKF = true
		}
		newHead, _ := r.win.Head()
		if newHead == head {
			return
		}
		head = newHead
	}
}

// windowUnderPressure reports whether admitting incoming would overflow the
// ring, i.e. there is no room to keep waiting for a reordered packet.
func (r *Recover) windowUnderPressure(incoming uint16) bool {
	head, ok := r.win.Head()
	if !ok {
		return false
	}
	return seqDistance(head, incoming) >= int32(r.win.capacity)
}

// recordGap appends a Gap spanning run missing seqs starting at head.
func (r *Recover) recordGap(head uint16, run int) {
	start, end := r.gapTickRange(head, run)
	r.gaps = append(r.gaps, Gap{
		TrackID:  r.cfg.TrackID,
		StartSeq: head,
		EndSeq:   head + uint16(run) - 1,
		Ticks:    clock.Range{Start: start, End: end},
		Length:   run,
	})
}

// gapTickRange resolves a run-length gap's time range starting at head. With
// packet-based timestamp mapping the 16-bit seq cannot index time across a
// wrap, so the range is derived from the most recently emitted tick and the
// per-step increment; otherwise the seq mapping is used directly.
func (r *Recover) gapTickRange(head uint16, run int) (start, end int64) {
	if r.tickOfPkt == nil {
		return r.tickOfSeq(head), r.tickOfSeq(head+uint16(run)) + 1
	}
	if !r.haveLast {
		return r.tickOfSeq(head), r.tickOfSeq(head+uint16(run)) + 1
	}
	step := r.stepTick
	if step <= 0 {
		step = 1
	}
	start = r.lastTick + step
	end = start + step*int64(run)
	return start, end
}

// noteObserved advances the high-water sequence mark across wrap boundaries.
func (r *Recover) noteObserved(seq uint16) {
	if !r.haveObsMax || seqDistance(r.maxObsSeq, seq) > 0 {
		r.maxObsSeq = seq
		r.haveObsMax = true
	}
}

// Drain emits every currently orderable sample, attaching gap markers when a
// burst of at least BurstThreshold consecutive packets is missing.
func (r *Recover) Drain() ([]Sample, error) {
	var out []Sample
	if len(r.earlySamples) > 0 {
		out = append(out, r.earlySamples...)
		r.earlySamples = nil
	}
	flushErr := r.win.Flush(func(pkt *rtp.Packet) error {
		s, err := r.makeSample(pkt)
		if err != nil {
			return err
		}
		out = append(out, s)
		return nil
	})
	if flushErr != nil {
		return nil, flushErr
	}
	more, err := r.advanceHoles()
	if err != nil {
		return nil, err
	}
	return append(out, more...), nil
}

// advanceHoles skips confirmed holes at the head. A hole is confirmed only
// once a real packet behind it sits in the window, proving the missing
// sequence numbers are lost rather than merely reordered. Sub-threshold runs
// keep waiting; threshold-sized runs become a Gap and are rotated past
// without producing any synthetic frames (video then awaits a keyframe).
func (r *Recover) advanceHoles() ([]Sample, error) {
	var out []Sample
	for {
		first := r.win.FirstFilled()
		if first <= 0 {
			return out, nil // empty window (nothing observed) or head is ready
		}
		if first < r.cfg.BurstThreshold {
			return out, nil // keep buffering; reorder may still fill the hole
		}
		head, _ := r.win.Head()
		if r.win.EvictConfirmedHoles(first) != first {
			return out, nil
		}
		r.recordGap(head, first)
		if r.cfg.IsVideo {
			r.waitingKF = true
		}
		if err := r.win.Flush(func(pkt *rtp.Packet) error {
			s, err := r.makeSample(pkt)
			if err != nil {
				return err
			}
			out = append(out, s)
			return nil
		}); err != nil {
			return nil, err
		}
	}
}

// Close flushes at end-of-stream. Packets still missing at this point are
// confirmed gaps (their packets will never arrive) following the same
// no-fabrication rules as mid-stream holes.
func (r *Recover) Close() ([]Sample, error) {
	var out []Sample
	if len(r.earlySamples) > 0 {
		out = append(out, r.earlySamples...)
		r.earlySamples = nil
	}
	more, err := r.advanceHoles()
	if err != nil {
		return nil, err
	}
	out = append(out, more...)
	// A tail loss can leave the window empty, so FirstFilled cannot see it;
	// the still-missing expectation is derived from the highest accepted seq.
	if r.haveObsMax {
		want := r.maxObsSeq + 1
		head, started := r.win.Head()
		if started && seqDistance(head, want) > 0 {
			length := int(seqDistance(head, want))
			gStart, gEnd := r.gapTickRange(head, length)
			g := Gap{
				TrackID:  r.cfg.TrackID,
				StartSeq: head,
				EndSeq:   want - 1,
				Ticks:    clock.Range{Start: gStart, End: gEnd},
				Length:   length,
			}
			for i := 0; i < length; i++ {
				r.win.SkipHead()
			}
			r.gaps = append(r.gaps, g)
			if r.cfg.IsVideo {
				r.waitingKF = true
			}
		}
	}
	if first := r.win.FirstFilled(); first > 0 {
		head, _ := r.win.Head()
		gStart, gEnd := r.gapTickRange(head, first)
		g := Gap{
			TrackID:  r.cfg.TrackID,
			StartSeq: head,
			EndSeq:   head + uint16(first) - 1,
			Ticks:    clock.Range{Start: gStart, End: gEnd},
			Length:   first,
		}
		for i := 0; i < first; i++ {
			r.win.SkipHead()
		}
		r.gaps = append(r.gaps, g)
		if r.cfg.IsVideo {
			r.waitingKF = true
		}
	}
	if err := r.win.Flush(func(pkt *rtp.Packet) error {
		s, err := r.makeSample(pkt)
		if err != nil {
			return err
		}
		out = append(out, s)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Recover) makeSample(pkt *rtp.Packet) (Sample, error) {
	env, err := ParseEnvelope(pkt.Payload)
	if err != nil {
		return Sample{}, err
	}
	tick := r.tickOf(pkt)
	if r.haveLast && tick <= r.lastTick {
		return Sample{}, clock.NewError(clock.KindNonMonotonic, "rtprecover",
			r.cfg.TrackID, tick, "recovered sample time not monotonic")
	}
	s := Sample{
		Seq:       pkt.SequenceNumber,
		TrackID:   r.cfg.TrackID,
		AtTick:    tick,
		Keyframe:  env.Flags&FlagKeyframe != 0,
		WaitingKF: r.waitingKF && !pktHasKeyframe(env.Flags),
		Flags:     env.Flags,
		Body:      env.Body,
	}
	if s.Keyframe {
		s.WaitingKF = false
	}
	r.lastTick, r.lastSeq, r.haveLast = tick, pkt.SequenceNumber, true
	return s, nil
}

func pktHasKeyframe(flags byte) bool { return flags&FlagKeyframe != 0 }

// Gaps returns a copy of all confirmed burst gaps.
func (r *Recover) Gaps() []Gap {
	out := make([]Gap, len(r.gaps))
	copy(out, r.gaps)
	return out
}

// Stats merges window counters with gap information.
func (r *Recover) Stats() Stats {
	st := r.win.Stats()
	return st
}

// HeadForTest exposes the playout head for diagnostics and tests.
func (r *Recover) HeadForTest() (uint16, bool) { return r.win.Head() }

// FirstFilledForTest exposes the first buffered offset for diagnostics.
func (r *Recover) FirstFilledForTest() int { return r.win.FirstFilled() }

// HeldForTest exposes the current buffer fill for diagnostics.
func (r *Recover) HeldForTest() int { return r.win.Held() }
