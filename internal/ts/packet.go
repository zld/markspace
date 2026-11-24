// Package ts parses ISO/IEC 13818-1 transport streams.
//
// The scanner in this file is deliberately stream-oriented: a 2 GiB master
// never has to fit in memory, and every diagnostic carries the PID, the byte
// offset and the packet index of the offending packet so operators can jump
// straight to the damage.
package ts

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

const (
	// PacketSize is the fixed MPEG-TS packet length.
	PacketSize = 188
	// SyncByte starts every transport packet.
	SyncByte byte = 0x47
	// MaxFileBytes is the supported single-file limit (2 GiB).
	MaxFileBytes int64 = 2 << 30
	// MaxPackets bounds one scan, keeping index and manifest sizes finite.
	MaxPackets int64 = 8_000_000
)

// Config controls scanner limits.
type Config struct {
	MaxFileBytes int64
	MaxPackets   int64
	// StrictPayload requires each packet header and adaptation field to be
	// internally consistent before its payload is exposed.
	StrictPayload bool
}

// DefaultConfig returns supported limits.
func DefaultConfig() Config {
	return Config{MaxFileBytes: MaxFileBytes, MaxPackets: MaxPackets, StrictPayload: true}
}

// Adaptation is the parsed adaptation field of one packet.
type Adaptation struct {
	Present       bool
	Length        int
	Discontinuity bool
	RandomAccess  bool
	PCR           bool
	PCRBase       uint64
	PCRExtension  uint16
	OPCR          bool
	OPCRBase      uint64
	StuffingBytes int
}

// Header is the transport packet header plus adaptation facts.
type Header struct {
	TransportError bool
	PayloadStart   bool
	Priority       bool
	PID            uint16
	Scrambling     uint8
	AFC            uint8
	Continuity     uint8
	Adaptation     Adaptation
	HasPayload     bool
	PayloadOffset  int
}

// Packet is one scanned transport packet. Payload is a copy owned by the
// consumer; Raw holds the original 188 bytes for provenance and duplicate
// comparison.
type Packet struct {
	Header
	Offset           int64
	Index            int64
	Raw              [PacketSize]byte
	Payload          []byte
	Duplicate        bool
	ContinuityBroken bool
}

// DiagnosticKind enumerates transport layer faults.
type DiagnosticKind string

const (
	DiagSyncLost        DiagnosticKind = "sync_lost"
	DiagTruncatedPacket DiagnosticKind = "truncated_packet"
	DiagContinuityJump  DiagnosticKind = "continuity_jump"
	DiagDuplicatePacket DiagnosticKind = "duplicate_packet"
	DiagTransportError  DiagnosticKind = "transport_error"
	DiagScrambledPacket DiagnosticKind = "scrambled_packet"
	DiagMalformedPacket DiagnosticKind = "malformed_packet"
)

// Diagnostic is a located transport fault.
type Diagnostic struct {
	Kind    DiagnosticKind
	PID     uint16
	Offset  int64
	Packet  int64
	PrevCC  uint8
	CC      uint8
	Skipped int64
	Detail  string
}

// Error implements error.
func (d Diagnostic) Error() string {
	loc := fmt.Sprintf("%s pid=0x%04x offset=%d packet=%d", d.Kind, d.PID, d.Offset, d.Packet)
	if d.Kind == DiagContinuityJump {
		loc += fmt.Sprintf(" prev_cc=%d cc=%d", d.PrevCC, d.CC)
	}
	if d.Kind == DiagSyncLost {
		loc += fmt.Sprintf(" skipped=%d", d.Skipped)
	}
	if d.Detail != "" {
		loc += " " + d.Detail
	}
	return loc
}

// Stats summarizes a scan.
type Stats struct {
	Bytes          int64
	Packets        int64
	PayloadPackets int64
	Duplicates     int64
	Continuity     int64
	SyncSkipped    int64
	Truncated      int64
	TransportErrs  int64
}

type pidCC struct {
	have    bool
	lastCC  uint8
	lastRaw [PacketSize]byte
	lastPay bool
}

// Scanner walks a TS file, recovering alignment and validating continuity.
type Scanner struct {
	f     *os.File
	cfg   Config
	size  int64
	pos   int64
	index int64

	diags []Diagnostic
	stats Stats

	cc map[uint16]*pidCC
	// chunk holds the lookahead window for resynchronization checks.
	chunk []byte
}

// NewScanner opens path read-only. The input is never modified.
func NewScanner(path string, cfg Config) (*Scanner, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("ts: %s is a directory", path)
	}
	if st.Size() > cfg.MaxFileBytes {
		return nil, fmt.Errorf("ts: %s is %d bytes, limit %d", path, st.Size(), cfg.MaxFileBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &Scanner{f: f, cfg: cfg, size: st.Size(), cc: make(map[uint16]*pidCC)}, nil
}

// Close releases the file.
func (s *Scanner) Close() error {
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}

// Diagnostics returns all located faults collected so far.
func (s *Scanner) Diagnostics() []Diagnostic { return s.diags }

// Stats returns the counters collected so far.
func (s *Scanner) Stats() Stats { return s.stats }

func (s *Scanner) diag(d Diagnostic) {
	s.diags = append(s.diags, d)
}

// Next returns the next packet. ok is false at a clean end of file. A
// truncated tail produces its diagnostic and then ok=false.
func (s *Scanner) Next() (*Packet, bool, error) {
	for {
		if s.pos+PacketSize > s.size {
			if s.pos < s.size {
				s.stats.Truncated++
				s.diag(Diagnostic{Kind: DiagTruncatedPacket, Offset: s.pos, Packet: s.index,
					Skipped: s.size - s.pos, Detail: "incomplete packet at end of file"})
				s.pos = s.size
			}
			return nil, false, nil
		}
		raw, err := s.read(s.pos)
		if err != nil {
			return nil, false, err
		}
		if raw[0] == SyncByte {
			return s.parse(raw, s.pos, s.index)
		}
		next, skipped, err := s.findSync(s.pos)
		if err != nil {
			return nil, false, err
		}
		s.stats.SyncSkipped += skipped
		s.diag(Diagnostic{Kind: DiagSyncLost, Offset: s.pos, Packet: s.index,
			Skipped: skipped, Detail: "resynchronized on next 188-byte packet boundary"})
		s.pos = next
		// Retry the loop with the recovered position.
	}
}

func (s *Scanner) read(off int64) ([PacketSize]byte, error) {
	var raw [PacketSize]byte
	if _, err := s.f.ReadAt(raw[:], off); err != nil {
		return raw, err
	}
	return raw, nil
}

func (s *Scanner) byteAt(off int64) (byte, error) {
	var b [1]byte
	if _, err := s.f.ReadAt(b[:], off); err != nil {
		return 0, err
	}
	return b[0], nil
}

// findSync looks for the next offset where a sync byte is followed, one
// packet later, by another sync byte. Requiring the confirmation stops payload
// bytes that happen to equal 0x47 from being mistaken for a packet boundary.
// A candidate whose packet is the last exactly-fitting packet of the file is
// accepted without lookahead. It returns the candidate and bytes skipped.
func (s *Scanner) findSync(from int64) (int64, int64, error) {
	start := from
	// Scan file bytes in windows to keep resynchronization O(bytes/64KiB)
	// rather than one syscall per byte.
	const window = 64 << 10
	buf := make([]byte, window)
	for base := from + 1; base+PacketSize <= s.size; base += window {
		n := int64(window)
		if base+n > s.size {
			n = s.size - base
		}
		if _, err := s.f.ReadAt(buf[:n], base); err != nil {
			return 0, 0, err
		}
		for i := int64(0); i < n; i++ {
			cand := base + i
			if cand+PacketSize > s.size {
				break
			}
			if buf[i] != SyncByte {
				continue
			}
			if cand+PacketSize == s.size {
				return cand, cand - start, nil
			}
			var nb [1]byte
			if _, err := s.f.ReadAt(nb[:], cand+PacketSize); err != nil {
				return 0, 0, err
			}
			if nb[0] == SyncByte {
				return cand, cand - start, nil
			}
		}
	}
	return s.size, s.size - start, nil
}

func (s *Scanner) parse(raw [PacketSize]byte, off, idx int64) (*Packet, bool, error) {
	p := &Packet{Offset: off, Index: idx}
	p.Raw = raw
	s.stats.Bytes += PacketSize
	s.pos = off + PacketSize
	s.index = idx + 1
	s.stats.Packets++
	if s.cfg.MaxPackets > 0 && s.index > s.cfg.MaxPackets {
		return nil, false, fmt.Errorf("ts: packet limit %d exceeded at offset %d", s.cfg.MaxPackets, off)
	}

	p.TransportError = raw[1]&0x80 != 0
	p.PayloadStart = raw[1]&0x40 != 0
	p.Priority = raw[1]&0x20 != 0
	p.PID = binary.BigEndian.Uint16(raw[1:3]) & 0x1FFF
	p.Scrambling = (raw[3] >> 6) & 3
	p.AFC = (raw[3] >> 4) & 3
	p.Continuity = raw[3] & 0x0F

	if p.TransportError {
		s.stats.TransportErrs++
		s.diag(Diagnostic{Kind: DiagTransportError, PID: p.PID, Offset: off, Packet: idx,
			Detail: "transport_error_indicator set"})
	}
	if p.Scrambling != 0 {
		s.diag(Diagnostic{Kind: DiagScrambledPacket, PID: p.PID, Offset: off, Packet: idx,
			Detail: fmt.Sprintf("scrambling_control=%d", p.Scrambling)})
	}

	if p.AFC == 0 {
		s.stats.Continuity++
		s.diag(Diagnostic{Kind: DiagMalformedPacket, PID: p.PID, Offset: off, Packet: idx,
			Detail: "adaptation_field_control=0"})
		return p, true, nil
	}

	payloadStart := 4
	if p.AFC&2 != 0 {
		alen := int(raw[4])
		if 5+alen > PacketSize {
			s.stats.Continuity++
			s.diag(Diagnostic{Kind: DiagMalformedPacket, PID: p.PID, Offset: off, Packet: idx,
				Detail: fmt.Sprintf("adaptation_field_length=%d exceeds packet", alen)})
			return p, true, nil
		}
		if alen > 0 {
			flags := raw[5]
			p.Adaptation.Present = true
			p.Adaptation.Length = alen
			p.Adaptation.Discontinuity = flags&0x80 != 0
			p.Adaptation.RandomAccess = flags&0x40 != 0
			read := 1
			if flags&0x10 != 0 && alen >= 1+6 {
				b := raw[6:12]
				p.Adaptation.PCR = true
				p.Adaptation.PCRBase = uint64(b[0])<<25 | uint64(b[1])<<17 | uint64(b[2])<<9 |
					uint64(b[3])<<1 | uint64(b[4])>>7
				p.Adaptation.PCRExtension = uint16(b[4]&1)<<8 | uint16(b[5])
				read += 6
			}
			if flags&0x08 != 0 && alen >= read+6 {
				b := raw[6+read : 12+read]
				p.Adaptation.OPCR = true
				p.Adaptation.OPCRBase = uint64(b[0])<<25 | uint64(b[1])<<17 | uint64(b[2])<<9 |
					uint64(b[3])<<1 | uint64(b[4])>>7
				read += 6
			}
		}
		payloadStart = 5 + alen
	}
	p.HasPayload = p.AFC&1 != 0 && payloadStart < PacketSize
	if p.HasPayload {
		if payloadStart > PacketSize {
			s.stats.Continuity++
			s.diag(Diagnostic{Kind: DiagMalformedPacket, PID: p.PID, Offset: off, Packet: idx,
				Detail: "payload_offset beyond packet"})
			return p, true, nil
		}
		p.PayloadOffset = payloadStart
		p.Payload = append([]byte(nil), raw[payloadStart:]...)
		s.stats.PayloadPackets++
	}

	if p.AFC&1 != 0 {
		s.checkContinuity(p, raw)
	}
	return p, true, nil
}

func (s *Scanner) checkContinuity(p *Packet, raw [PacketSize]byte) {
	st := s.cc[p.PID]
	if st == nil {
		st = &pidCC{}
		s.cc[p.PID] = st
	}
	if !st.have {
		st.have = true
		st.lastCC = p.Continuity
		st.lastRaw = raw
		st.lastPay = p.HasPayload
		return
	}
	if p.Continuity == st.lastCC {
		if raw == st.lastRaw {
			p.Duplicate = true
			s.stats.Duplicates++
			s.diag(Diagnostic{Kind: DiagDuplicatePacket, PID: p.PID, Offset: p.Offset, Packet: p.Index,
				PrevCC: st.lastCC, CC: p.Continuity, Detail: "identical repeat of previous packet"})
			return
		}
		s.stats.Continuity++
		p.ContinuityBroken = true
		s.diag(Diagnostic{Kind: DiagContinuityJump, PID: p.PID, Offset: p.Offset, Packet: p.Index,
			PrevCC: st.lastCC, CC: p.Continuity, Detail: "repeated CC with different payload"})
	} else {
		want := (st.lastCC + 1) & 0x0F
		if p.Continuity != want {
			s.stats.Continuity++
			p.ContinuityBroken = true
			s.diag(Diagnostic{Kind: DiagContinuityJump, PID: p.PID, Offset: p.Offset, Packet: p.Index,
				PrevCC: st.lastCC, CC: p.Continuity,
				Detail: fmt.Sprintf("expected CC %d", want)})
		}
	}
	st.lastCC = p.Continuity
	st.lastRaw = raw
	st.lastPay = p.HasPayload
}

// ReadAll scans a file and invokes fn for every delivered packet. Duplicates
// are delivered with Duplicate set; callers that assemble payloads must skip
// them. The returned diagnostics include malformed and truncated tails.
func ReadAll(path string, cfg Config, fn func(*Packet) error) (Stats, []Diagnostic, error) {
	s, err := NewScanner(path, cfg)
	if err != nil {
		return Stats{}, nil, err
	}
	defer s.Close()
	for {
		p, ok, err := s.Next()
		if err != nil {
			return s.Stats(), s.Diagnostics(), err
		}
		if !ok {
			break
		}
		if fn != nil {
			if err := fn(p); err != nil {
				return s.Stats(), s.Diagnostics(), err
			}
		}
	}
	return s.Stats(), s.Diagnostics(), nil
}

// ReadPacket reads the 188-byte packet at a known source offset. It is used
// by the index-driven remuxer to fetch exactly the packets it cites, rather
// than trusting a sequential reread.
func ReadPacket(f *os.File, off int64) (*Packet, error) {
	if off < 0 || off%1 != 0 {
		return nil, fmt.Errorf("ts: invalid packet offset %d", off)
	}
	var raw [PacketSize]byte
	if _, err := f.ReadAt(raw[:], off); err != nil {
		return nil, err
	}
	if raw[0] != SyncByte {
		return nil, fmt.Errorf("ts: no sync byte at offset %d", off)
	}
	// A degenerate parse for payload extraction: no continuity state needed.
	p := &Packet{Offset: off, Raw: raw}
	p.PID = binary.BigEndian.Uint16(raw[1:3]) & 0x1FFF
	p.PayloadStart = raw[1]&0x40 != 0
	p.AFC = (raw[3] >> 4) & 3
	p.Continuity = raw[3] & 0x0F
	if p.AFC == 0 || p.AFC&1 == 0 {
		return p, nil
	}
	start := 4
	if p.AFC&2 != 0 {
		start = 5 + int(raw[4])
	}
	if start >= PacketSize {
		return p, nil
	}
	p.HasPayload = true
	p.PayloadOffset = start
	p.Payload = append([]byte(nil), raw[start:]...)
	return p, nil
}

var _ io.Reader = (*os.File)(nil)
