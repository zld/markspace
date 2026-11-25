// Package rtp turns an out-of-order, lossy RTP receive trace into a stable
// ordered view.
//
// Packets are de-duplicated by sequence number, re-ordered inside a bounded
// window, and adjacent missing sequence numbers are grouped into damaged
// spans once the window can no longer accept them. A timestamp that jumps
// backwards by at least half of the 32-bit space is classified as a counter
// wrap rather than a reorder.
//
// Once the window advances past a position that position is final: a copy
// arriving later is counted as late and never mutates the committed view.
// That is what lets a staged pipeline checkpoint its progress without later
// input rewriting history.
package rtp

import (
	"fmt"

	prtp "github.com/pion/rtp"

	"replaycheck/internal/fault"
)

// Config controls windowing.
type Config struct {
	// Window is how many sequence numbers past the highest accepted packet
	// stay open. Positions further back are committed and can no longer be
	// filled by late packets.
	Window int
	// ClockRate converts timestamps to milliseconds.
	ClockRate int
	// WrapSlack is the backward timestamp jump that counts as a wrap. Zero
	// means half of the 32-bit timestamp space.
	WrapSlack int64
}

// DefaultConfig suits a 90 kHz video track with a generous reorder window.
func DefaultConfig() Config {
	return Config{Window: 512, ClockRate: 90000}
}

// Packet is an ordered view entry.
type Packet struct {
	Seq uint16
	// ExtSeq is Seq on the monotone extended axis; it orders packets across
	// a 16-bit rollover and must be used for span arithmetic.
	ExtSeq      int64
	Timestamp   uint32
	Marker      bool
	PayloadType uint8
	Payload     []byte
	// Arrival is the zero-based index in the receive trace.
	Arrival int
}

// Span is a maximal run of committed sequence numbers. A span with Lost > 0
// carries one interior gap between GapStart and GapEnd.
type Span struct {
	First    uint16
	Last     uint16
	Lost     int
	GapStart uint16
	GapEnd   uint16
	// GapStartTick and GapEndTick are RTP timestamps of the packets
	// bracketing the gap. They are meaningful only when Lost > 0.
	GapStartTick uint32
	GapEndTick   uint32
	// GapStartSeq/GapEndSeq are the extended sequence numbers on either
	// side of a gap, used to place it on the media timeline precisely.
	GapStartSeq int64
	GapEndSeq   int64
}

// Result is the full analysis of a trace.
type Result struct {
	Packets   []Packet
	Committed []Span
	Issues    fault.List

	Received  int
	Duplicate int
	Reordered int
	// Lost counts sequence numbers inside the observed range that never
	// arrived.
	Lost  int
	Wraps int
	// Late counts packets dropped because their position was final.
	Late int
	// FirstTimestamp, LastTimestamp bracket the accepted packets.
	FirstTimestamp uint32
	LastTimestamp  uint32
	HasTimestamps  bool
	// ClockRate is the timestamp clock the result was produced at.
	ClockRate int
}

// Reorderer consumes packets incrementally and commits ordered positions.
type Reorderer struct {
	cfg  Config
	seen map[uint16]Packet
	done map[uint16]bool

	lowest  int64 // smallest extended sequence accepted
	highest int64 // largest extended sequence accepted
	scanAt  int64 // next extended position not yet committed
	final   int64 // positions below this are final; -1<<62 before start

	hasAny        bool
	lastTimestamp uint32
	hasTimestamp  bool

	result      Result
	committedTS map[uint16]uint32
}

// New returns an empty reorderer.
func New(cfg Config) *Reorderer {
	if cfg.Window <= 0 {
		cfg.Window = 512
	}
	if cfg.ClockRate <= 0 {
		cfg.ClockRate = 90000
	}
	return &Reorderer{
		cfg:         cfg,
		seen:        make(map[uint16]Packet),
		done:        make(map[uint16]bool),
		final:       -(1 << 62),
		committedTS: make(map[uint16]uint32),
	}
}

// Decode parses one wire-format RTP packet.
func Decode(buf []byte) (*prtp.Packet, error) {
	p := new(prtp.Packet)
	if err := p.Unmarshal(buf); err != nil {
		return nil, fmt.Errorf("rtp: malformed packet: %w", err)
	}
	return p, nil
}

// Push consumes one packet copy. Duplicates, reorders and late arrivals in
// any order are counted without disturbing the committed view.
func (r *Reorderer) Push(p *prtp.Packet) {
	r.result.Received++
	ext := r.extend(p.SequenceNumber)

	if r.hasAny && ext < r.final {
		// A copy of a packet already in the committed view is a duplicate;
		// a position that was never delivered is a late arrival. Neither may
		// change what has been committed.
		if r.done[p.SequenceNumber] {
			r.result.Duplicate++
			return
		}
		r.result.Late++
		r.result.Issues.Add(fault.New(fault.KindStageStale, fault.Warn,
			fault.ByteAt(0), "late packet seq %d (extended %d) after final position %d",
			p.SequenceNumber, ext, r.final))
		return
	}
	if _, exists := r.seen[p.SequenceNumber]; exists {
		r.result.Duplicate++
		return
	}

	r.observeTimestamp(p.Timestamp, p.SequenceNumber)

	if !r.hasAny {
		r.hasAny = true
		r.lowest, r.highest = ext, ext
		r.scanAt = ext
		r.result.FirstTimestamp = p.Timestamp
		r.result.HasTimestamps = true
	} else {
		if ext < r.lowest {
			r.lowest = ext
			r.scanAt = ext
		}
		if ext > r.highest {
			r.highest = ext
		} else if ext < r.highest {
			r.result.Reordered++
		}
		r.result.LastTimestamp = p.Timestamp
	}

	r.seen[p.SequenceNumber] = Packet{
		Seq:         p.SequenceNumber,
		ExtSeq:      ext,
		Timestamp:   p.Timestamp,
		Marker:      p.Marker,
		PayloadType: p.PayloadType,
		Payload:     append([]byte(nil), p.Payload...),
		Arrival:     r.result.Received - 1,
	}
	r.advance()
}

func (r *Reorderer) observeTimestamp(ts uint32, seq uint16) {
	if !r.hasTimestamp {
		r.lastTimestamp = ts
		r.hasTimestamp = true
		return
	}
	if clockWrapped(r.lastTimestamp, ts, r.cfg.WrapSlack) {
		r.result.Wraps++
		r.result.Issues.Add(fault.New(fault.KindTimestampWrap, fault.Warn,
			fault.ByteAt(0), "timestamp wrap at seq %d: %d -> %d",
			seq, r.lastTimestamp, ts))
	}
	r.lastTimestamp = ts
}

// extend maps a 16-bit sequence number onto a monotone extended axis around
// the highest observed value.
func (r *Reorderer) extend(seq uint16) int64 {
	if !r.hasAny {
		return int64(seq)
	}
	base := r.highest &^ 0xFFFF
	ext := base + int64(seq)
	if ext < r.highest-0x8000 {
		ext += 0x10000
	} else if ext > r.highest+0x8000 {
		ext -= 0x10000
	}
	return ext
}

// advance commits every position that the window can no longer fill.
func (r *Reorderer) advance() {
	if !r.hasAny {
		return
	}
	edge := r.highest - int64(r.cfg.Window)
	if edge < r.scanAt {
		return
	}
	for seq := r.scanAt; seq <= edge; seq++ {
		r.commit(seq)
	}
	r.scanAt = edge + 1
	// Everything below the scan frontier is final: a copy arriving for such
	// a position can no longer change the committed view.
	if r.scanAt > r.final {
		r.final = r.scanAt
	}
}

// Finish closes the window and commits everything from the earliest
// uncommitted position through the highest observed.
func (r *Reorderer) Finish() *Result {
	if !r.hasAny {
		r.result.ClockRate = r.cfg.ClockRate
		return &r.result
	}
	for seq := r.scanAt; seq <= r.highest; seq++ {
		r.commit(seq)
	}
	r.scanAt = r.highest + 1
	if r.scanAt > r.final {
		r.final = r.scanAt
	}
	r.result.Committed = groupSpans(r.result.Packets)
	annotateSpans(r.result.Committed, r.result.Packets)
	for _, sp := range r.result.Committed {
		if sp.Lost == 0 {
			continue
		}
		r.result.Lost += sp.Lost
		ms := TimestampMillis(r.timestampAt(sp.Last), r.cfg.ClockRate)
		r.result.Issues.Add(fault.New(fault.KindPacketLoss, fault.Damaged,
			fault.Location{ByteOffset: -1, Tick: ms, HasTick: true, Millis: ms, Label: "rtp"},
			"sequence gap %d..%d (%d packets missing) before seq %d at %dms",
			sp.GapStart, sp.GapEnd, sp.Lost, sp.Last, ms))
	}
	r.result.ClockRate = r.cfg.ClockRate
	return &r.result
}

func (r *Reorderer) commit(seq int64) {
	s := uint16(seq & 0xFFFF)
	pk, ok := r.seen[s]
	if !ok {
		return
	}
	r.result.Packets = append(r.result.Packets, pk)
	r.committedTS[s] = pk.Timestamp
	r.done[s] = true
	delete(r.seen, s)
}

func (r *Reorderer) timestampAt(seq uint16) uint32 {
	if ts, ok := r.committedTS[seq]; ok {
		return ts
	}
	return 0
}

// groupSpans groups packets into maximal consecutive runs. Packets must be
// in ascending committed (extended) order.
func groupSpans(pkts []Packet) []Span {
	if len(pkts) == 0 {
		return nil
	}
	spans := make([]Span, 0, 8)
	cur := Span{First: pkts[0].Seq, Last: pkts[0].Seq}
	lastExt := pkts[0].ExtSeq
	for _, p := range pkts[1:] {
		if p.ExtSeq == lastExt+1 {
			cur.Last = p.Seq
			lastExt = p.ExtSeq
			continue
		}
		spans = append(spans, closeSpan(cur, p, lastExt))
		cur = Span{First: p.Seq, Last: p.Seq}
		lastExt = p.ExtSeq
	}
	spans = append(spans, cur)
	return spans
}

// annotateSpans records the timestamps on either side of every gap.
func annotateSpans(spans []Span, pkts []Packet) {
	i := 0
	for si := range spans {
		// Packets are ordered on the extended axis; walk the run belonging
		// to this span until membership stops.
		n := 0
		for i+n < len(pkts) {
			if n > 0 && pkts[i+n].ExtSeq != pkts[i+n-1].ExtSeq+1 {
				break
			}
			n++
		}
		if n == 0 {
			continue
		}
		if spans[si].Lost > 0 {
			spans[si].GapStartTick = pkts[i+n-1].Timestamp
			if i+n < len(pkts) {
				spans[si].GapEndTick = pkts[i+n].Timestamp
			} else {
				spans[si].GapEndTick = pkts[i+n-1].Timestamp
			}
		}
		i += n
	}
}

// closeSpan records the gap between the span ending at cur and the next
// packet. The distance is computed on the extended axis so a rollover
// between 65535 and 0 is a one-step gap, not a 65535-step one.
func closeSpan(cur Span, next Packet, lastExt int64) Span {
	if gap := next.ExtSeq - lastExt - 1; gap > 0 {
		cur.Lost = int(gap)
		cur.GapStart = cur.Last + 1
		cur.GapEnd = next.Seq - 1
		cur.GapStartSeq = lastExt + 1
		cur.GapEndSeq = next.ExtSeq - 1
	}
	return cur
}

// clockWrapped reports a backward timestamp jump at least as large as the
// wrap threshold (half the 32-bit space unless configured otherwise).
func clockWrapped(prev, next uint32, slack int64) bool {
	if next >= prev {
		return false
	}
	limit := int64(1) << 31
	if slack > 0 {
		limit = slack
	}
	return int64(prev)-int64(next) >= limit
}

// TimestampMillis converts a timestamp to milliseconds at the given clock.
func TimestampMillis(ts uint32, clockRate int) int64 {
	if clockRate <= 0 {
		clockRate = 90000
	}
	return int64(ts) * 1000 / int64(clockRate)
}

// Summary renders the counters as a compact string used by the CLI.
func (res *Result) Summary() string {
	return fmt.Sprintf("received=%d dup=%d reordered=%d lost=%d wraps=%d late=%d spans=%d",
		res.Received, res.Duplicate, res.Reordered, res.Lost, res.Wraps, res.Late, len(res.Committed))
}
