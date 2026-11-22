// Package rtp parses RTP packets into a media-neutral form and reorders them
// per SSRC by sequence number and arrival time. Packets that fall outside the
// reorder window are reported as late rather than silently discarded, and a
// duplicate sequence number is never emitted twice.
package rtp

import (
	"sort"

	"github.com/pion/rtp"

	"example.com/mediaforge/internal/merrors"
)

// MaxPacketSize bounds a single RTP packet accepted by the parser.
const MaxPacketSize = 1200

// Packet is the parsed, media-neutral representation of one RTP packet.
type Packet struct {
	SSRC         uint32
	Seq          uint16
	Timestamp    uint32
	Marker       bool
	PayloadType  uint8
	Payload      []byte
	ArrivalNanos int64 // reference arrival time, monotonic per input
}

// Parse decodes buf as a single RTP packet. The returned payload is a copy so
// the caller may reuse buf. Total packet size is bounded by MaxPacketSize.
func Parse(buf []byte, arrivalNanos int64) (Packet, error) {
	if len(buf) > MaxPacketSize {
		return Packet{}, merrors.At(merrors.StageRTPParse, merrors.Bounds, int64(len(buf)),
			"packet size %d exceeds limit %d", len(buf), MaxPacketSize)
	}
	var pk rtp.Packet
	if err := pk.Unmarshal(buf); err != nil {
		cat := merrors.Truncated
		if len(buf) >= 12 {
			cat = merrors.Malformed
		}
		return Packet{}, merrors.Wrap(merrors.StageRTPParse, cat, int64(len(buf)), err,
			"rtp packet (%d bytes)", len(buf))
	}
	if pk.Version != 2 {
		return Packet{}, merrors.At(merrors.StageRTPParse, merrors.Malformed, 0,
			"unsupported RTP version %d", pk.Version)
	}
	payload := make([]byte, len(pk.Payload))
	copy(payload, pk.Payload)
	return Packet{
		SSRC:         pk.SSRC,
		Seq:          pk.SequenceNumber,
		Timestamp:    pk.Timestamp,
		Marker:       pk.Marker,
		PayloadType:  pk.PayloadType,
		Payload:      payload,
		ArrivalNanos: arrivalNanos,
	}, nil
}

// SeqDelta returns b-a as a signed 16-bit delta, correct across wrap-around.
func SeqDelta(b, a uint16) int {
	return int(int16(b - a))
}

// Config controls a per-SSRC reorderer.
type Config struct {
	// Window is how far ahead of the next expected sequence number a packet
	// may arrive and still be buffered. Older or newer packets than this are
	// reported as late.
	Window int
	// MaxBuffer bounds the number of packets held at once.
	MaxBuffer int
}

// DefaultConfig returns conservative bounds.
func DefaultConfig() Config {
	return Config{Window: 64, MaxBuffer: 4096}
}

func (c Config) validate() error {
	if c.Window <= 0 || c.Window > 1<<14 {
		return merrors.New(merrors.StageRTPReorder, merrors.Invalid,
			"reorder window %d outside 1..16384", c.Window)
	}
	if c.MaxBuffer <= 0 || c.MaxBuffer > 1<<20 {
		return merrors.New(merrors.StageRTPReorder, merrors.Invalid,
			"max buffer %d outside 1..1048576", c.MaxBuffer)
	}
	return nil
}

// Stats reports reorderer counters.
type Stats struct {
	Emitted   int
	Late      int
	Duplicate int
	Gaps      int
	MaxHeld   int
}

// Gap describes a missing run of sequence numbers the window gave up on.
type Gap struct {
	SSRC  uint32
	From  uint16 // first missing sequence number
	Count int
	// Contiguous is true when Count is a single run of absent packets.
	Contiguous bool
}

// Reorderer performs sliding-window sequence reordering for one SSRC.
type Reorderer struct {
	cfg     Config
	ssrc    uint32
	started bool
	nextSeq uint16
	highSeq uint16
	pending map[uint16]Packet
	// emitted remembers recently emitted sequence numbers, bounded to the
	// last 2*Window emissions, so duplicates can be classified precisely.
	emitted []uint16

	st Stats
}

// NewReorderer creates a reorderer bound to ssrc.
func NewReorderer(ssrc uint32, cfg Config) (*Reorderer, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Reorderer{
		cfg:     cfg,
		ssrc:    ssrc,
		pending: make(map[uint16]Packet),
	}, nil
}

// Stats returns a snapshot of counters.
func (r *Reorderer) Stats() Stats { return r.st }

func (r *Reorderer) wasEmitted(seq uint16) bool {
	for _, s := range r.emitted {
		if s == seq {
			return true
		}
	}
	return false
}

func (r *Reorderer) markEmitted(seq uint16) {
	r.emitted = append(r.emitted, seq)
	if keep := 2 * r.cfg.Window; len(r.emitted) > keep {
		r.emitted = r.emitted[len(r.emitted)-keep:]
	}
}

// Push accepts a packet and returns the packets now safe to emit, in
// sequence order, plus any gaps the window has given up on.
//
// A packet is "late" when the reorder window has already slid past its
// sequence number, i.e. it is more than Window behind nextSeq; such packets
// cannot be placed without corrupting output order and are reported, not
// emitted. Duplicate sequence numbers are reported separately and are never
// emitted twice. Packets arriving ahead of the window are simply held.
func (r *Reorderer) Push(p Packet) (out []Packet, gaps []Gap, err error) {
	if p.SSRC != r.ssrc {
		return nil, nil, merrors.Packet(merrors.StageRTPReorder, merrors.Invalid,
			p.SSRC, p.Seq, "packet for unexpected SSRC")
	}
	if !r.started {
		r.started = true
		r.nextSeq = p.Seq
		r.highSeq = p.Seq
		r.pending[p.Seq] = p
		return r.advance()
	}

	if r.wasEmitted(p.Seq) {
		r.st.Duplicate++
		return nil, nil, merrors.Packet(merrors.StageRTPReorder, merrors.Duplicate,
			p.SSRC, p.Seq, "duplicate sequence (already emitted)")
	}
	if r.hasPending(p.Seq) {
		r.st.Duplicate++
		return nil, nil, merrors.Packet(merrors.StageRTPReorder, merrors.Duplicate,
			p.SSRC, p.Seq, "duplicate sequence (buffered)")
	}
	if rel := SeqDelta(p.Seq, r.nextSeq); rel < -r.cfg.Window {
		r.st.Late++
		return nil, nil, merrors.Packet(merrors.StageRTPReorder, merrors.Late,
			p.SSRC, p.Seq, "behind window (next=%d window=%d)", r.nextSeq, r.cfg.Window)
	}
	if len(r.pending) >= r.cfg.MaxBuffer {
		return nil, nil, merrors.Packet(merrors.StageRTPReorder, merrors.Overflow,
			p.SSRC, p.Seq, "buffer full (%d)", len(r.pending))
	}

	r.pending[p.Seq] = p
	if SeqDelta(p.Seq, r.highSeq) > 0 {
		r.highSeq = p.Seq
	}
	if len(r.pending) > r.st.MaxHeld {
		r.st.MaxHeld = len(r.pending)
	}
	return r.advance()
}

func (r *Reorderer) hasPending(seq uint16) bool {
	_, ok := r.pending[seq]
	return ok
}

// advance emits the contiguous run starting at nextSeq. A hole may only be
// declared once a packet more than Window ahead of it has arrived: at that
// point any packet that could fill the hole would itself be late.
func (r *Reorderer) advance() (out []Packet, gaps []Gap, err error) {
	for {
		if p, ok := r.pending[r.nextSeq]; ok {
			delete(r.pending, r.nextSeq)
			r.markEmitted(r.nextSeq)
			out = append(out, p)
			r.st.Emitted++
			r.nextSeq++
			continue
		}
		if SeqDelta(r.highSeq, r.nextSeq) <= r.cfg.Window {
			return out, gaps, nil
		}
		// Coalesce the whole unreachable run into a single gap record. The
		// first hole is past the window, so every sequence up to the next
		// buffered packet is past it as well.
		start := r.nextSeq
		for !r.hasPending(r.nextSeq) && SeqDelta(r.highSeq, r.nextSeq) > 0 {
			r.nextSeq++
		}
		if missing := SeqDelta(r.nextSeq, start); missing > 0 {
			r.st.Gaps++
			gaps = append(gaps, Gap{SSRC: r.ssrc, From: start, Count: missing, Contiguous: true})
		}
	}
}

// Flush ends the stream. Buffered packets are emitted in sequence order and
// every trailing hole below the highest sequence seen is reported as its own
// gap record.
func (r *Reorderer) Flush() (out []Packet, gaps []Gap) {
	if !r.started {
		return nil, nil
	}
	for {
		if p, ok := r.pending[r.nextSeq]; ok {
			delete(r.pending, r.nextSeq)
			r.markEmitted(r.nextSeq)
			out = append(out, p)
			r.st.Emitted++
			r.nextSeq++
			continue
		}
		if SeqDelta(r.highSeq, r.nextSeq) <= 0 {
			break
		}
		r.st.Gaps++
		gaps = append(gaps, Gap{SSRC: r.ssrc, From: r.nextSeq, Count: 1, Contiguous: true})
		r.nextSeq++
	}
	// Anything still buffered is ahead of highSeq only if highSeq was stale;
	// emit deterministically anyway so nothing is silently dropped.
	if len(r.pending) > 0 {
		rest := make([]Packet, 0, len(r.pending))
		for _, p := range r.pending {
			rest = append(rest, p)
		}
		sort.Slice(rest, func(i, j int) bool { return SeqDelta(rest[i].Seq, rest[j].Seq) < 0 })
		for _, p := range rest {
			out = append(out, p)
			r.st.Emitted++
		}
		r.pending = make(map[uint16]Packet)
	}
	return out, gaps
}

// SortByArrival orders packets by arrival time then sequence, giving a
// deterministic order for equal arrival stamps.
func SortByArrival(pkts []Packet) {
	sort.SliceStable(pkts, func(i, j int) bool {
		if pkts[i].ArrivalNanos != pkts[j].ArrivalNanos {
			return pkts[i].ArrivalNanos < pkts[j].ArrivalNanos
		}
		return SeqDelta(pkts[i].Seq, pkts[j].Seq) < 0
	})
}
