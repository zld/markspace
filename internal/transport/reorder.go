// Package transport turns a lossy RTP packet stream into an ordered timeline
// per SSRC.  It deduplicates packets, expands the 16-bit sequence number into
// a monotonically increasing extended sequence, tolerates reordering within a
// bounded window and records every missing run as a continuous gap.
package transport

import (
	"errors"

	"github.com/pion/rtp"

	"mediapipeline/internal/clock"
	"mediapipeline/internal/fault"
)

// MaxWindow bounds how far ahead of the next expected sequence a packet may
// arrive and still be accepted.  It is also the reorder buffer capacity.
const MaxWindow = 1024

// Packet is a received RTP packet after header parsing.
type Packet struct {
	SSRC      uint32
	Sequence  uint16 // raw 16-bit RTP sequence number
	Timestamp uint32 // RTP timestamp in the SSRC clock
	Payload   []byte
	Arrival   int // arrival ordinal, used only for diagnostics
}

// OrderedPacket is a packet emitted in sequence order, with the extended
// (wrap-around expanded) sequence number attached.
type OrderedPacket struct {
	Packet
	Extended uint64
}

// Gap is a continuous run of missing extended sequence numbers.  It is the
// half-open range [From, To): From is the first missing sequence, To is the
// first sequence after the run.
type Gap struct {
	SSRC uint32
	From uint64
	To   uint64
}

// Count returns the number of missing packets in the gap.
func (g Gap) Count() uint64 { return g.To - g.From }

// Session holds the reorder state for one SSRC.
type Session struct {
	SSRC         uint32
	baseExt      uint64
	haveBase     bool
	refExt       uint64 // extended sequence of the highest sequence observed
	nextExpected uint64 // lowest extended sequence not yet emitted
	emitted      bool   // whether any packet has been emitted
	buf          map[uint64]Packet
	out          []OrderedPacket // ready packets awaiting Drain
	duplicates   int
	gaps         []Gap
	zero         clock.Rational
	rc           *Reorder
}

func newSession(ssrc uint32, rc *Reorder) *Session {
	return &Session{
		SSRC: ssrc,
		buf:  make(map[uint64]Packet),
		zero: clock.MustRational(0, 1),
		rc:   rc,
	}
}

// Reorder keeps one Session per SSRC and processes an interleaved feed.
type Reorder struct {
	sessions  map[uint32]*Session
	maxWindow uint64
	report    *fault.Report
}

// NewReorder builds a reorder buffer; report receives duplicate and gap faults.
func NewReorder(report *fault.Report) (*Reorder, error) {
	if report == nil {
		return nil, errors.New("transport: nil fault report")
	}
	return &Reorder{
		sessions:  make(map[uint32]*Session),
		maxWindow: MaxWindow,
		report:    report,
	}, nil
}

// expand maps a raw sequence number to an extended one using the distance from
// the last established reference, choosing the representation with the
// smallest signed 16-bit distance so wrap-around is handled correctly.  It
// may return a value below ref for a late packet.
func expand(ref uint64, seq uint16) uint64 {
	delta := int64(int16(seq - uint16(ref)))
	return uint64(int64(ref) + delta)
}

// Push parses one raw RTP datagram with pion/rtp and feeds it to the matching
// SSRC session.  Duplicate and out-of-window packets are rejected with faults
// and never reach the ordered output.
func (r *Reorder) Push(raw []byte, arrival int) error {
	var p rtp.Packet
	if err := p.Unmarshal(raw); err != nil {
		return err
	}
	s := r.sessions[p.SSRC]
	if s == nil {
		s = newSession(p.SSRC, r)
		r.sessions[p.SSRC] = s
	}
	return s.push(Packet{
		SSRC:      p.SSRC,
		Sequence:  p.SequenceNumber,
		Timestamp: p.Timestamp,
		Payload:   append([]byte(nil), p.Payload...),
		Arrival:   arrival,
	}, r)
}

func (s *Session) push(p Packet, r *Reorder) error {
	if !s.haveBase {
		// Establish the session on whatever arrives first; ordering below the
		// base is still deduplicated against nextExpected.
		s.baseExt = uint64(p.Sequence)
		s.refExt = s.baseExt
		s.nextExpected = s.baseExt
		s.haveBase = true
	}
	// Expand against the highest observed sequence: the signed 16-bit delta
	// picks the closest representation, which is correct across wrap-around as
	// long as the jump stays within half the 16-bit space.
	ext := expand(s.refExt, p.Sequence)

	if ext < s.baseExt && !s.emitted {
		// Nothing has been emitted yet, so an earlier packet may legally
		// re-anchor the start of the session instead of being a stale
		// duplicate.  Lower the expected position to the smaller sequence and
		// keep anything already buffered above it.
		s.baseExt = ext
		s.nextExpected = ext
		s.buf[ext] = p
		return nil
	}

	// A packet beyond the reorder window means the expected position is stuck
	// on real loss.  Finalise the buffered run into gaps, resync onto the far
	// packet and continue; the loss is reported, not silently absorbed.
	if ext > s.refExt+s.rc.maxWindow {
		r.report.Addf(fault.CodeWindowExceeded, int64(p.Arrival), s.zero, s.zero,
			"ssrc=%d seq=%d is beyond reorder window (next=%d), resynchronising",
			p.SSRC, p.Sequence, s.nextExpected)
		s.finalizeIntoGap(r, ext)
		s.baseExt = ext
		s.nextExpected = ext
		s.refExt = ext
		s.emitted = true
		s.buf[ext] = p
		return nil
	}

	if ext < s.nextExpected {
		s.duplicates++
		r.report.Addf(fault.CodeDuplicatePacket, int64(p.Arrival), s.zero, s.zero,
			"ssrc=%d ext=%d already delivered (next=%d)", p.SSRC, ext, s.nextExpected)
		return nil
	}
	if _, exists := s.buf[ext]; exists {
		s.duplicates++
		r.report.Addf(fault.CodeDuplicatePacket, int64(p.Arrival), s.zero, s.zero,
			"ssrc=%d ext=%d already buffered", p.SSRC, ext)
		return nil
	}
	if ext > s.refExt {
		s.refExt = ext
	}
	s.buf[ext] = p
	return nil
}

// finalizeIntoGap records the missing runs below far and moves every buffered
// packet below far into the ready list, preserving arrival order.  It is used
// to close a stuck gap and resynchronise onto a far-ahead packet.
func (s *Session) finalizeIntoGap(r *Reorder, far uint64) {
	exts := make([]uint64, 0, len(s.buf))
	for ext := range s.buf {
		exts = append(exts, ext)
	}
	sortUint64(exts)
	cursor := s.nextExpected
	for _, ext := range exts {
		if ext >= far {
			continue
		}
		if ext > cursor {
			s.recordGap(r, cursor, ext-cursor)
		}
		s.out = append(s.out, OrderedPacket{Packet: s.buf[ext], Extended: ext})
		cursor = ext + 1
	}
	if far > cursor {
		s.recordGap(r, cursor, far-cursor)
	}
	s.buf = make(map[uint64]Packet)
	s.emitted = true
}

func sortUint64(v []uint64) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

// recordGap appends a continuous gap of count packets at from.
func (s *Session) recordGap(r *Reorder, from, count uint64) {
	if count == 0 {
		return
	}
	g := Gap{SSRC: s.SSRC, From: from, To: from + count}
	s.gaps = append(s.gaps, g)
	r.report.Addf(fault.CodeGap, -1, s.zero, s.zero,
		"ssrc=%d missing ext seq %d..%d (%d packets)",
		s.SSRC, g.From, g.To-1, g.Count())
}

// advanceGap records the hole between nextExpected and the smallest buffered
// packet greater than it, then moves nextExpected up to that packet.  It is
// used to bound the buffer when the expected position cannot advance.
func (s *Session) advanceGap(r *Reorder) {
	min := ^uint64(0)
	for ext := range s.buf {
		if ext > s.nextExpected && ext < min {
			min = ext
		}
	}
	if min == ^uint64(0) {
		return
	}
	s.recordGap(r, s.nextExpected, min-s.nextExpected)
	s.nextExpected = min
}

// Drain emits every currently contiguous packet starting at nextExpected and
// records the gaps that separate emitted runs.
func (r *Reorder) Drain(ssrc uint32) []OrderedPacket {
	s := r.sessions[ssrc]
	if s == nil {
		return nil
	}
	for {
		p, ok := s.buf[s.nextExpected]
		if !ok {
			break
		}
		delete(s.buf, s.nextExpected)
		s.out = append(s.out, OrderedPacket{Packet: p, Extended: s.nextExpected})
		s.nextExpected++
		s.emitted = true
	}
	out := s.out
	s.out = nil
	return out
}

// Flush finalises a session: any still-buffered packets imply gaps before them.
// Gaps are emitted as continuous ranges in extended sequence space and the
// buffered packets follow in order.
func (r *Reorder) Flush(ssrc uint32) []OrderedPacket {
	s := r.sessions[ssrc]
	if s == nil {
		return nil
	}
	out := r.Drain(ssrc)
	exts := make([]uint64, 0, len(s.buf))
	for ext := range s.buf {
		exts = append(exts, ext)
	}
	sortUint64(exts)
	cursor := s.nextExpected
	for _, ext := range exts {
		if ext > cursor {
			s.recordGap(r, cursor, ext-cursor)
		}
		out = append(out, OrderedPacket{Packet: s.buf[ext], Extended: ext})
		cursor = ext + 1
	}
	s.buf = make(map[uint64]Packet)
	s.nextExpected = cursor
	return out
}

// Gaps returns the continuous gaps recorded for one SSRC.
func (r *Reorder) Gaps(ssrc uint32) []Gap {
	if s := r.sessions[ssrc]; s != nil {
		out := make([]Gap, len(s.gaps))
		copy(out, s.gaps)
		return out
	}
	return nil
}

// TotalMissing returns the number of missing packets across every SSRC.
func (r *Reorder) TotalMissing() uint64 {
	var n uint64
	for _, s := range r.sessions {
		for _, g := range s.gaps {
			n += g.Count()
		}
	}
	return n
}

// Duplicates returns the duplicate packet count for one SSRC.
func (r *Reorder) Duplicates(ssrc uint32) int {
	if s := r.sessions[ssrc]; s != nil {
		return s.duplicates
	}
	return 0
}

// SSRCs lists the observed synchronisation sources.
func (r *Reorder) SSRCs() []uint32 {
	out := make([]uint32, 0, len(r.sessions))
	for ssrc := range r.sessions {
		out = append(out, ssrc)
	}
	return out
}
