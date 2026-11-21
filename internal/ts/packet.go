// Package ts implements a small, allocation-conscious MPEG-TS reader:
// packet synchronisation, PAT/PMT parsing and PES header extraction.
package ts

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// MPEG-TS packet geometry and well-known identifiers.
const (
	PacketSize = 188
	SyncByte   = 0x47

	PIDPAT  uint16 = 0x0000 // program association table
	PIDNull uint16 = 0x1fff // null/filler packets

	// MaxEvents bounds how many scanner diagnostics are retained so a
	// pathological stream cannot exhaust memory.
	MaxEvents = 10_000
)

// EventKind enumerates the diagnostics a Scanner can emit without failing.
type EventKind int

const (
	// EventResync: bytes were skipped to reacquire the 0x47 sync byte.
	EventResync EventKind = iota
	// EventCorrupt: transport error indicator set or malformed header.
	EventCorrupt
	// EventCCError: per-PID continuity counter did not follow the rules.
	EventCCError
	// EventTruncated: trailing bytes shorter than one full packet.
	EventTruncated
)

// String renders the event kind for reports.
func (k EventKind) String() string {
	switch k {
	case EventResync:
		return "resync"
	case EventCorrupt:
		return "corrupt_packet"
	case EventCCError:
		return "continuity_error"
	case EventTruncated:
		return "truncated_trailer"
	default:
		return "unknown"
	}
}

// Event is one locatable scanner diagnostic.
type Event struct {
	Kind    EventKind
	PID     uint16
	Seq     int64 // global packet sequence number
	Skipped int   // bytes discarded during resync
	WantCC  uint8
	GotCC   uint8
}

// Packet is one parsed 188-byte transport stream packet.
type Packet struct {
	Data       []byte
	PID        uint16
	Seq        int64
	PUSI       bool // payload unit start indicator
	Priority   bool
	TEI        bool // transport error indicator
	HasAdapt   bool
	HasPayload bool
	CC         uint8
	DiscFlag   bool // discontinuity indicator in adaptation field
}

// ParsePacket parses one 188-byte buffer. The buffer length is validated and
// the sync byte is checked; malformed adaptation fields are reported via the
// returned error but never panic.
func ParsePacket(b []byte) (Packet, error) {
	if len(b) != PacketSize {
		return Packet{}, errors.New("ts: packet must be 188 bytes")
	}
	if b[0] != SyncByte {
		return Packet{}, errors.New("ts: missing sync byte")
	}
	p := Packet{
		Data:       b,
		TEI:        b[1]&0x80 != 0,
		PUSI:       b[1]&0x40 != 0,
		Priority:   b[1]&0x20 != 0,
		PID:        uint16(b[1]&0x1f)<<8 | uint16(b[2]),
		HasAdapt:   b[3]&0x20 != 0,
		HasPayload: b[3]&0x10 != 0,
		CC:         b[3] & 0x0f,
	}
	// 0x1FFF is reserved for null/filler packets which carry no payload or
	// adaptation field. Anything else using it is misframed, which also
	// rejects false 0x47 candidates encountered while resynchronising.
	if p.PID == PIDNull && (p.HasAdapt || p.HasPayload) {
		return p, errors.New("ts: reserved PID 0x1fff with payload or adaptation")
	}
	if p.HasAdapt {
		al := int(b[4])
		if al > PacketSize-5 {
			return p, errors.New("ts: adaptation field length exceeds packet")
		}
		if al >= 1 {
			p.DiscFlag = b[5]&0x80 != 0
		}
	}
	return p, nil
}

// Payload returns the payload slice, skipping any adaptation field. It is
// empty for adaptation-only and null packets.
func (p Packet) Payload() []byte {
	if !p.HasPayload || p.PID == PIDNull {
		return nil
	}
	off := 4
	if p.HasAdapt {
		off = 5 + int(p.Data[4])
	}
	if off > PacketSize {
		return nil
	}
	return p.Data[off:]
}

type ccState struct {
	cc  uint8
	has bool
}

// Scanner synchronises an arbitrary byte stream onto TS packet boundaries,
// parses each packet and tracks per-PID continuity counters.
type Scanner struct {
	br     *bufio.Reader
	seq    int64
	cc     map[uint16]ccState
	events []Event
	err    error
}

// NewScanner wraps r; a buffered reader is added when r is not one already.
func NewScanner(r io.Reader) *Scanner {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReaderSize(r, PacketSize*16)
	}
	return &Scanner{br: br, cc: make(map[uint16]ccState)}
}

// Next returns the next packet, or nil at end of stream. Diagnostics for
// resyncs, corruption and continuity errors are retained as Events rather
// than stopping the scan; damaged input never panics.
func (s *Scanner) Next() *Packet {
	if s.err != nil {
		return nil
	}
	skipped := s.acquireSync()
	if s.err != nil {
		if errors.Is(s.err, io.ErrUnexpectedEOF) {
			s.record(Event{Kind: EventTruncated, Seq: s.seq})
		}
		return nil
	}
	if skipped > 0 {
		s.record(Event{Kind: EventResync, Seq: s.seq, Skipped: skipped})
	}
	buf := make([]byte, PacketSize)
	if _, err := io.ReadFull(s.br, buf); err != nil {
		s.record(Event{Kind: EventTruncated, Seq: s.seq})
		s.err = err
		return nil
	}
	pkt, perr := ParsePacket(buf)
	if perr != nil {
		// acquireSync already validated the header; keep the packet marked
		// as corrupt but continue from the next sync byte on the next call.
		s.record(Event{Kind: EventCorrupt, PID: pkt.PID, Seq: s.seq})
		return s.Next()
	}
	if pkt.TEI {
		s.record(Event{Kind: EventCorrupt, PID: pkt.PID, Seq: s.seq})
	}
	pkt.Seq = s.seq
	s.seq++
	s.checkCC(&pkt)
	return &pkt
}

// acquireSync advances the buffer until a structurally plausible TS packet
// starts at the head, leaving its sync byte buffered for ReadFull. It uses
// Peek only, so rejected candidates can be retried without rewind logic.
func (s *Scanner) acquireSync() int {
	skipped := 0
	for {
		peek, err := s.br.Peek(PacketSize)
		if err != nil {
			if errors.Is(err, io.EOF) && s.br.Buffered() == 0 {
				s.err = io.EOF
			} else if (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) &&
				len(peek) > 0 && peek[0] == SyncByte {
				s.err = io.ErrUnexpectedEOF
			} else {
				s.err = err
			}
			return skipped
		}
		idx := bytes.IndexByte(peek, SyncByte)
		if idx > 0 {
			if _, derr := s.br.Discard(idx); derr != nil {
				s.err = derr
				return skipped
			}
			skipped += idx
			continue
		}
		// Candidate at head: confirm a successor sync one packet later,
		// or fully validate the header for an end-of-stream candidate.
		more, merr := s.br.Peek(2 * PacketSize)
		confirmed := merr == nil && more[PacketSize] == SyncByte
		if confirmed || (merr != nil && packetValid(peek)) {
			return skipped
		}
		if merr != nil && !errors.Is(merr, io.EOF) &&
			!errors.Is(merr, io.ErrUnexpectedEOF) {
			s.err = merr
			return skipped
		}
		// False 0x47 (or invalid end-of-stream header): discard and hunt.
		if _, derr := s.br.Discard(1); derr != nil {
			s.err = derr
			return skipped
		}
		skipped++
	}
}

// packetValid reports whether the buffered candidate parses cleanly.
func packetValid(b []byte) bool {
	_, err := ParsePacket(b)
	return err == nil
}

func (s *Scanner) checkCC(p *Packet) {
	if p.PID == PIDNull {
		return
	}
	prev, known := s.cc[p.PID]
	if p.DiscFlag || !known {
		s.cc[p.PID] = ccState{cc: p.CC, has: true}
		return
	}
	// Adaptation-only packets repeat the previous CC; payload-bearing
	// packets must increment it by one modulo 16.
	if p.HasPayload && (prev.cc+1)&0x0f != p.CC {
		s.record(Event{Kind: EventCCError, PID: p.PID, Seq: p.Seq,
			WantCC: (prev.cc + 1) & 0x0f, GotCC: p.CC})
	}
	s.cc[p.PID] = ccState{cc: p.CC, has: true}
}

func (s *Scanner) record(e Event) {
	if len(s.events) < MaxEvents {
		s.events = append(s.events, e)
	}
}

// Events returns the retained diagnostics (capped at MaxEvents).
func (s *Scanner) Events() []Event { return s.events }

// Seq returns the number of packets successfully parsed so far.
func (s *Scanner) Seq() int64 { return s.seq }

// Err returns nil on clean EOF, otherwise the first terminal I/O error.
func (s *Scanner) Err() error {
	if errors.Is(s.err, io.EOF) || errors.Is(s.err, io.ErrUnexpectedEOF) {
		return nil
	}
	return s.err
}
