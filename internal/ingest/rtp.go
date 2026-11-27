// Package ingest parses local RTP/RTCP captures into ordered, clocked
// packets. It never fabricates payload: a lost packet becomes a deficit
// entry, and a packet arriving outside the reorder window is recorded as
// a late deficit rather than retroactively inserted.
package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/pion/rtp"

	"replay/internal/fault"
)

// DefaultWindow is how many sequence slots behind the high-water mark a
// packet may arrive and still be reordered into place. Anything further
// behind is late and becomes a deficit.
const DefaultWindow = 64

// Deficit reasons.
const (
	// ReasonGap marks a sequence slot that was never received.
	ReasonGap = "gap"
	// ReasonLate marks a packet that arrived after its slot had already
	// been passed by the reorder window.
	ReasonLate = "late"
)

// Packet is one accepted RTP packet with its unwrapped position.
type Packet struct {
	Track      string
	SSRC       uint32
	Seq        uint16
	Extended   uint64
	Timestamp  uint32
	Marker     bool
	PayloadLen int
	Index      int
	// PayloadHash is a stable digest of the payload used for duplicate
	// conflict detection. Payload bytes themselves are not retained.
	PayloadHash string
}

// Deficit records a missing sequence number.
type Deficit struct {
	Track    string
	SSRC     uint32
	Extended uint64
	Reason   string
}

// Duplicate records a repeated sequence number.
type Duplicate struct {
	Track       string
	SSRC        uint32
	Extended    uint64
	Seq         uint16
	SamePayload bool
	// Late is set when the duplicate showed up after its slot had been
	// flushed.
	Late bool
}

// Stream is the deterministic result of ingesting one SSRC.
type Stream struct {
	Track      string
	SSRC       uint32
	Packets    []Packet
	Deficits   []Deficit
	Duplicates []Duplicate
	Wraps      int
	// Received counts every accepted, non-duplicate packet.
	Received int
	// Late counts packets that arrived outside the reorder window.
	Late int
}

// PacketCount returns accepted packets.
func (s *Stream) PacketCount() int { return len(s.Packets) }

// DeficitCount returns missing sequence numbers.
func (s *Stream) DeficitCount() int { return len(s.Deficits) }

// DuplicateCount returns repeated packets.
func (s *Stream) DuplicateCount() int { return len(s.Duplicates) }

// DuplicateConflicts returns duplicates whose payload differed from the
// accepted original. These are content conflicts, not benign repeats.
func (s *Stream) DuplicateConflicts() []Duplicate {
	var out []Duplicate
	for _, d := range s.Duplicates {
		if !d.SamePayload {
			out = append(out, d)
		}
	}
	return out
}

// GapCount returns deficits caused by a numbering gap rather than a late
// arrival.
func (s *Stream) GapCount() int {
	n := 0
	for _, d := range s.Deficits {
		if d.Reason == ReasonGap {
			n++
		}
	}
	return n
}

// LateCount returns deficits caused by late arrivals.
func (s *Stream) LateCount() int {
	n := 0
	for _, d := range s.Deficits {
		if d.Reason == ReasonLate {
			n++
		}
	}
	return n
}

// Parser reorders packets for a set of SSRCs. Create one Parser per
// capture and call Add for each datagram in input order, then Finish.
type Parser struct {
	track   string
	window  uint64
	streams map[uint32]*streamState
	order   []uint32
	index   int
}

type streamState struct {
	ssrc    uint32
	started bool
	highest uint64
	seen    map[uint64]bool
	pending map[uint64]Packet
	out     Stream
	flushed int
}

// NewParser creates a parser for one track's RTP datagrams with the
// default reorder window.
func NewParser(track string, clockRate uint32) (*Parser, error) {
	return NewParserWindow(track, clockRate, DefaultWindow)
}

// NewParserWindow creates a parser with an explicit reorder window in
// sequence slots. clockRate is validated here and recorded by the
// caller when it builds the track clock.
func NewParserWindow(track string, clockRate uint32, window uint64) (*Parser, error) {
	if track == "" {
		return nil, fault.New(fault.KindArgument, "RTP parser requires a track ID").At("ingest")
	}
	if clockRate == 0 {
		return nil, fault.New(fault.KindArgument, "RTP parser requires a positive clock rate").
			At("ingest").WithTrack(track)
	}
	if window == 0 {
		window = DefaultWindow
	}
	return &Parser{
		track:   track,
		window:  window,
		streams: make(map[uint32]*streamState),
	}, nil
}

// Window returns the configured reorder window.
func (p *Parser) Window() uint64 { return p.window }

// Add parses and accepts one RTP datagram. offset is the byte position
// of the datagram in the input stream and is reported on parse errors.
func (p *Parser) Add(data []byte, offset int64) error {
	var pkt rtp.Packet
	if err := pkt.Unmarshal(data); err != nil {
		return fault.New(fault.KindInput, "malformed rtp packet").
			At("ingest").WithTrack(p.track).WithOffset(offset).WithIndex(p.index).Wrap(err)
	}
	if len(pkt.Payload) == 0 {
		return fault.New(fault.KindInput, "empty rtp payload").
			At("ingest").WithTrack(p.track).WithOffset(offset).WithIndex(p.index).
			WithSSRC(pkt.SSRC).WithSeq(uint64(pkt.SequenceNumber))
	}
	st := p.streamFor(pkt.SSRC)
	if err := st.accept(&pkt, p.index, p.window); err != nil {
		fe, ok := fault.As(err)
		if ok {
			return fe.WithTrack(p.track).WithIndex(p.index).WithOffset(offset)
		}
		return err
	}
	p.index++
	return nil
}

func (p *Parser) streamFor(ssrc uint32) *streamState {
	st, ok := p.streams[ssrc]
	if !ok {
		st = &streamState{
			ssrc:    ssrc,
			seen:    make(map[uint64]bool),
			pending: make(map[uint64]Packet),
		}
		st.out.Track = p.track
		st.out.SSRC = ssrc
		p.streams[ssrc] = st
		p.order = append(p.order, ssrc)
	}
	return st
}

func (st *streamState) accept(pkt *rtp.Packet, index int, window uint64) error {
	seq := pkt.SequenceNumber
	ext := st.extend(seq)
	payloadHash := hashPayload(pkt.Payload)

	if st.seen[ext] {
		original, pending := st.pending[ext]
		same := pending && original.PayloadHash == payloadHash
		late := !pending
		if late {
			st.out.Late++
		}
		st.out.Duplicates = append(st.out.Duplicates, Duplicate{
			Track:       st.out.Track,
			SSRC:        st.ssrc,
			Extended:    ext,
			Seq:         seq,
			SamePayload: same,
			Late:        late,
		})
		// A duplicate with a different payload is a content conflict.
		// It is recorded (and the original kept) rather than fatal, so
		// the merge stage can report and quarantine it.
		return nil
	}

	// A packet more than `window` slots behind the high-water mark is
	// late: record it as a deficit and never reinsert it.
	if st.started && ext+window < st.highest {
		st.seen[ext] = true
		st.out.Late++
		st.out.Deficits = append(st.out.Deficits, Deficit{
			Track:    st.out.Track,
			SSRC:     st.ssrc,
			Extended: ext,
			Reason:   ReasonLate,
		})
		return nil
	}

	st.seen[ext] = true
	st.pending[ext] = Packet{
		Track:       st.out.Track,
		SSRC:        pkt.SSRC,
		Seq:         seq,
		Extended:    ext,
		Timestamp:   pkt.Timestamp,
		Marker:      pkt.Marker,
		PayloadLen:  len(pkt.Payload),
		Index:       index,
		PayloadHash: payloadHash,
	}
	st.out.Received++
	return nil
}

// extend converts a 16-bit sequence number to a 64-bit slot relative to
// the highest seen value, detecting 16-bit wrap.
func (st *streamState) extend(seq uint16) uint64 {
	if !st.started {
		st.started = true
		st.highest = uint64(seq)
		return uint64(seq)
	}
	diff := int64(int16(seq - uint16(st.highest)))
	ext := uint64(int64(st.highest) + diff)
	if ext > st.highest {
		if ext>>16 != st.highest>>16 {
			st.out.Wraps += int(ext>>16 - st.highest>>16)
		}
		st.highest = ext
	}
	return ext
}

// Finish flushes pending packets in extended sequence order and records
// gaps as deficits. The returned streams are ordered by SSRC so identical
// input yields identical output.
func (p *Parser) Finish() []Stream {
	ssrcs := append([]uint32(nil), p.order...)
	sort.Slice(ssrcs, func(i, j int) bool { return ssrcs[i] < ssrcs[j] })

	out := make([]Stream, 0, len(ssrcs))
	for _, ssrc := range ssrcs {
		st := p.streams[ssrc]
		st.flush()
		out = append(out, st.out)
	}
	return out
}

func (st *streamState) flush() {
	if len(st.pending) == 0 {
		return
	}
	exts := make([]uint64, 0, len(st.pending))
	for ext := range st.pending {
		exts = append(exts, ext)
	}
	sort.Slice(exts, func(i, j int) bool { return exts[i] < exts[j] })

	for i, ext := range exts {
		if i > 0 && ext > exts[i-1]+1 {
			st.out.Deficits = append(st.out.Deficits, gapDeficits(st.out.Track, st.ssrc, exts[i-1]+1, ext-1)...)
		}
		st.out.Packets = append(st.out.Packets, st.pending[ext])
	}
	st.flushed = len(st.out.Packets)
}

// gapDeficits emits one deficit per missing slot in [from, to]. The
// caller never fabricates a packet for these slots.
func gapDeficits(track string, ssrc uint32, from, to uint64) []Deficit {
	if to < from {
		return nil
	}
	n := int(to-from) + 1
	out := make([]Deficit, 0, n)
	for ext := from; ext <= to; ext++ {
		out = append(out, Deficit{Track: track, SSRC: ssrc, Extended: ext, Reason: ReasonGap})
		if ext == ^uint64(0) {
			break
		}
	}
	return out
}

// hashPayload returns a lowercase hex SHA-256 of the payload. Only the
// digest is retained; payload bytes never leave the parser.
func hashPayload(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
