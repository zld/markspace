// Package ingest turns raw MPEG-TS bytes (plain or length-prefixed RTP
// datagrams) into packet records stamped with the unified 27 MHz time base.
// All reading is streaming: one 188-byte packet at a time, never whole files.
package ingest

import (
	"bufio"
	"errors"
	"io"

	"tsindex/internal/clock"
	"tsindex/internal/fault"
)

// PacketSize is the fixed MPEG-TS packet length.
const PacketSize = 188

// SyncByte marks the start of every MPEG-TS packet.
const SyncByte = 0x47

// PID assignments reserved by the standard.
const (
	PIDPAT = 0x0000
	PIDCAT = 0x0001
	PIDTSDT = 0x0002
)

// Packet is one demarcated 188-byte transport packet.
type Packet struct {
	Index      int64    // ordinal packet number in the source stream
	Offset     int64    // source byte offset of the sync byte
	PID        uint16
	PUSI       bool
	CC         uint8
	HasPayload bool
	HasAdapt   bool
	Discontinuity bool
	PCR        int64
	HasPCR     bool
	Raw        [PacketSize]byte
}

// Payload returns the payload slice inside Raw (nil when absent).
func (p *Packet) Payload() []byte {
	off := 4
	if p.HasAdapt {
		off += 1 + int(p.Raw[4])
	}
	if !p.HasPayload || off >= PacketSize {
		return nil
	}
	return p.Raw[off:]
}

// Record is a packet with its unified timestamp, if one is known. The clock
// never invents stamps: HasTime is false until the first PCR has been seen.
type Record struct {
	Packet
	Ticks   int64
	HasTime bool
}

// Segment is a fixed-size, complete run of packets handed to the workers.
// Partial final runs are never emitted, so the pool only sees full segments.
type Segment struct {
	ID          int64
	StartOffset int64
	EndOffset   int64
	Records     []Record
}

// StreamReader performs packet-level demarcation, PCR expansion and
// continuity-counter auditing over an arbitrary byte stream.
type StreamReader struct {
	r      *bufio.Reader
	rec    *fault.Recorder
	clk    *clock.Clock
	tl     clock.Timeline
	offset int64
	index  int64
	cc     map[uint16]uint8
	ccSeen map[uint16]bool
}

// NewStreamReader wraps r with the shared fault recorder and unified clock.
func NewStreamReader(r io.Reader, rec *fault.Recorder) *StreamReader {
	return &StreamReader{
		r:      bufio.NewReaderSize(r, 64*PacketSize),
		rec:    rec,
		clk:    clock.New(),
		cc:     make(map[uint16]uint8),
		ccSeen: make(map[uint16]bool),
	}
}

// Clocks exposes the unified clock (wrap count etc.).
func (s *StreamReader) Clocks() *clock.Clock { return s.clk }

func readFull(r io.Reader, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := r.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// NextPacket reads one packet. io.EOF is returned at a clean boundary; a
// short final read yields a recoverable KindTruncated fault before EOF.
func (s *StreamReader) NextPacket() (*Record, error) {
	var raw [PacketSize]byte
	start := s.offset
	n, err := readFull(s.r, raw[:])
	if errors.Is(err, io.EOF) {
		if n == 0 {
			return nil, io.EOF
		}
		s.rec.Record(fault.Fault{Kind: fault.KindTruncated, Offset: start,
			Recoverable: true, Detail: "partial TS packet at end of input"})
		return nil, io.EOF
	}
	if err != nil {
		return nil, err
	}
	s.offset += PacketSize

	if raw[0] != SyncByte {
		s.rec.Record(fault.Fault{Kind: fault.KindCorruptPacket, Offset: start,
			Recoverable: true, Detail: "missing 0x47 sync byte"})
	}
	p := parseHeader(start, s.index, &raw)
	s.index++
	s.auditCC(&p)

	rec := Record{Packet: p}
	if p.HasPCR {
		ticks, wrapped, cerr := s.clk.Expand(p.PCR)
		if cerr != nil {
			s.rec.Record(fault.Fault{Kind: fault.KindCorruptPacket, Offset: start,
				Recoverable: true, Detail: cerr.Error()})
		}
		if wrapped {
			// Wrap is normal clock behaviour, recorded as an informational
			// fault so timeline epochs stay auditable.
			s.rec.Record(fault.Fault{Kind: fault.KindPSIUpdate, Offset: start,
				Recoverable: true, Detail: "PCR 2^33 wrap expanded at 27 MHz"})
		}
		rec.Ticks = s.tl.Update(start, ticks).Ticks
		rec.HasTime = true
	} else if anchor, ok := s.tl.At(start); ok {
		rec.Ticks = anchor.Ticks
		rec.HasTime = true
	}
	return &rec, nil
}

func parseHeader(offset, index int64, raw *[PacketSize]byte) Packet {
	p := Packet{Index: index, Offset: offset, Raw: *raw}
	p.PUSI = raw[1]&0x40 != 0
	p.PID = uint16(raw[1]&0x1F)<<8 | uint16(raw[2])
	p.CC = raw[3] & 0x0F
	afc := (raw[3] >> 4) & 0x03
	p.HasPayload = afc == 0x1 || afc == 0x3
	p.HasAdapt = afc == 0x2 || afc == 0x3
	if p.HasAdapt && raw[4] >= 1 && raw[4] <= 183 {
		flags := raw[5]
		p.Discontinuity = flags&0x80 != 0
		if flags&0x10 != 0 && raw[4] >= 7 {
			base := int64(raw[6])<<25 | int64(raw[7])<<17 | int64(raw[8])<<9 |
				int64(raw[9])<<1 | int64(raw[10])>>7
			ext := int64(raw[10]&0x01)<<8 | int64(raw[11])
			p.PCR = base*300 + ext
			p.HasPCR = true
		}
	}
	return p
}

// auditCC records discontinuities as holes. It never inserts or repairs
// packets: continuity counters are only observed, never rewritten.
func (s *StreamReader) auditCC(p *Packet) {
	if p.PID <= PIDTSDT {
		return // tables have their own section machinery
	}
	if s.ccSeen[p.PID] {
		prev := s.cc[p.PID]
		want := (prev + 1) & 0x0F
		if p.CC != want {
			if p.CC == prev && !p.HasPayload {
				// Legal duplicate of a payload-less packet.
			} else if p.Discontinuity {
				// Sender-declared discontinuity: still a hole in our timeline.
				s.rec.Record(fault.Fault{Kind: fault.KindCCGap, Offset: p.Offset,
					Recoverable: true, Detail: "declared CC discontinuity"})
			} else {
				s.rec.Record(fault.Fault{Kind: fault.KindCCGap, Offset: p.Offset,
					Recoverable: true, Detail: "continuity counter gap"})
			}
		}
	}
	s.cc[p.PID] = p.CC
	s.ccSeen[p.PID] = true
}

// NextSegment reads exactly packetsPerSegment packets. A trailing incomplete
// group is withheld (and any truncation already recorded); io.EOF means no
// complete segment remains.
func (s *StreamReader) NextSegment(packetsPerSegment int) (*Segment, error) {
	if packetsPerSegment <= 0 {
		return nil, errors.New("ingest: segment size must be positive")
	}
	seg := &Segment{Records: make([]Record, 0, packetsPerSegment)}
	for len(seg.Records) < packetsPerSegment {
		rec, err := s.NextPacket()
		if errors.Is(err, io.EOF) {
			if len(seg.Records) == 0 {
				return nil, io.EOF
			}
			// Incomplete tail: drop the partial segment without emitting it.
			return nil, io.EOF
		}
		if err != nil {
			return nil, err
		}
		if len(seg.Records) == 0 {
			seg.StartOffset = rec.Offset
		}
		seg.EndOffset = rec.Offset + PacketSize
		seg.Records = append(seg.Records, *rec)
	}
	seg.ID = seg.Records[0].Index / int64(packetsPerSegment)
	return seg, nil
}
