// Package ts implements a self-contained MPEG-TS (ISO/IEC 13818-1) reader
// for H.264/AAC streams: packet validation, continuity counters, PAT/PMT
// section assembly and PES header parsing.
package ts

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

// PacketSize is the fixed MPEG-TS packet length.
const PacketSize = 188

// Well-known PIDs and stream types.
const (
	PIDPAT        = 0x0000
	StreamH264    = 0x1B
	StreamAACADTS = 0x0F
	StreamAACLATM = 0x11
)

// Adaptation field control values.
const (
	AFCReserved = 0
	AFCPayload  = 1
	AFCOnly     = 2
	AFCBoth     = 3
)

var (
	// ErrSyncByte reports a missing 0x47 sync byte.
	ErrSyncByte = errors.New("ts: sync byte mismatch")
	// ErrTruncated reports fewer than 188 bytes at end of input.
	ErrTruncated = errors.New("ts: truncated packet")
)

// Packet is one parsed 188-byte transport packet.
type Packet struct {
	PID             uint16
	PUSI            bool
	Adaptation      uint8
	CC              uint8
	HasPayload      bool
	Payload         []byte // aliases the source buffer; valid until next Read
	AdaptationField []byte
}

// ParsePacket parses one 188-byte buffer.
func ParsePacket(b []byte) (Packet, error) {
	if len(b) < PacketSize {
		return Packet{}, fmt.Errorf("%w: got %d bytes", ErrTruncated, len(b))
	}
	if b[0] != 0x47 {
		return Packet{}, fmt.Errorf("%w: got 0x%02x", ErrSyncByte, b[0])
	}
	p := Packet{
		PUSI:       b[1]&0x40 != 0,
		PID:        uint16(b[1]&0x1F)<<8 | uint16(b[2]),
		Adaptation: (b[3] >> 4) & 0x03,
		CC:         b[3] & 0x0F,
	}
	p.HasPayload = p.Adaptation == AFCPayload || p.Adaptation == AFCBoth
	off := 4
	if p.Adaptation == AFCOnly || p.Adaptation == AFCBoth {
		al := int(b[off])
		if al > 0 {
			p.AdaptationField = b[off+1 : off+1+min(al, PacketSize-off-1)]
		}
		off += 1 + al
		if off > PacketSize {
			off = PacketSize
		}
	}
	if p.HasPayload && off < PacketSize {
		p.Payload = b[off:]
	}
	return p, nil
}

// CCError is a continuity counter discontinuity on one PID.
type CCError struct {
	PID      uint16
	Expected uint8
	Got      uint8
}

func (e CCError) Error() string {
	return fmt.Sprintf("ts: continuity counter jump on PID %#x: expected %d got %d", e.PID, e.Expected, e.Got)
}

// PESInfo holds the timing fields parsed from a PES header.
type PESInfo struct {
	StreamID byte
	PTS      int64 // in 90kHz units; -1 when absent
	DTS      int64
	HasPTS   bool
	HasDTS   bool
	Data     []byte // PES payload start inside the packet payload
}

// ParsePES parses a PES packet beginning at b (payload of a PUSI packet).
func ParsePES(b []byte) (PESInfo, error) {
	if len(b) < 9 || b[0] != 0x00 || b[1] != 0x00 || b[2] != 0x01 {
		return PESInfo{}, errors.New("ts: malformed PES start code")
	}
	info := PESInfo{StreamID: b[3], PTS: -1, DTS: -1}
	hdrLen := int(b[8])
	if len(b) < 9+hdrLen {
		return PESInfo{}, errors.New("ts: PES header longer than packet")
	}
	if hdrLen >= 3 {
		flags := b[7]
		prefix := b[9] >> 4
		switch prefix {
		case 2, 3: // PTS only, or PTS only (forbidden combo in practice 3)
			info.HasPTS = true
			info.PTS = decodeClock(b[9:14])
		case 8, 10, 1: // PTS+DTS
			info.HasPTS = true
			info.PTS = decodeClock(b[9:14])
			if hdrLen >= 8 {
				info.HasDTS = true
				info.DTS = decodeClock(b[14:19])
			}
		}
		_ = flags
	}
	info.Data = b[9+hdrLen:]
	return info, nil
}

// decodeClock decodes a 33-bit 90kHz timestamp from a 5-byte TS field.
func decodeClock(b []byte) int64 {
	return int64(b[0]&0x0E)<<29 | int64(b[1])<<22 |
		int64(b[2]&0xFE)<<14 | int64(b[3])<<7 | int64(b[4]>>1)
}

// section holds a PSI table being reassembled.
type section struct {
	buf     bytes.Buffer
	remain  int // payload bytes still expected
	started bool
}

// Parser is a streaming TS parser tracking CC state and PSI tables.
type Parser struct {
	r        *bufio.Reader
	cc       map[uint16]uint8
	ccSeen   map[uint16]bool
	sections map[uint16]*section
	// PMTPID is discovered from the PAT.
	PMTPID     uint16
	PMTSeen    bool
	Elementary map[uint16]byte // PID -> stream type
	videoPID   uint16
	audioPID   uint16
	scan       [PacketSize]byte
}

// NewParser constructs a parser over r.
func NewParser(r io.Reader) *Parser {
	return &Parser{
		r:          bufio.NewReaderSize(r, 16*PacketSize),
		cc:         map[uint16]uint8{},
		ccSeen:     map[uint16]bool{},
		sections:   map[uint16]*section{},
		Elementary: map[uint16]byte{},
	}
}

// VideoPID returns the detected H.264 PID, or 0.
func (p *Parser) VideoPID() uint16 { return p.videoPID }

// AudioPID returns the detected AAC PID, or 0.
func (p *Parser) AudioPID() uint16 { return p.audioPID }

// Read returns the next packet. io.EOF marks a clean end. A trailing partial
// packet is reported wrapped around ErrTruncated.
func (p *Parser) Read() (Packet, error) {
	for {
		if _, err := io.ReadFull(p.r, p.scan[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return Packet{}, io.EOF // clean boundary
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return Packet{}, ErrTruncated
			}
			return Packet{}, err
		}
		pkt, err := ParsePacket(p.scan[:])
		if err != nil {
			return Packet{}, err
		}
		if err := p.checkCC(pkt); err != nil {
			return pkt, err
		}
		if pkt.PID == PIDPAT || pkt.PID == p.PMTPID {
			p.feedSection(pkt)
		}
		return pkt, nil
	}
}

func (p *Parser) checkCC(pkt Packet) error {
	// Null packets and payload-free packets do not increment the CC.
	if pkt.PID == 0x1FFF || !pkt.HasPayload {
		return nil
	}
	last, seen := p.cc[pkt.PID], p.ccSeen[pkt.PID]
	if seen {
		want := (last + 1) & 0x0F
		if want != pkt.CC {
			return CCError{PID: pkt.PID, Expected: want, Got: pkt.CC}
		}
	}
	p.cc[pkt.PID] = pkt.CC
	p.ccSeen[pkt.PID] = true
	return nil
}

// RawPacket returns a copy of the 188 bytes behind the most recent packet
// returned by Read, so callers (the slicer) can re-emit packets verbatim.
func (p *Parser) RawPacket(pkt Packet) []byte {
	return append([]byte(nil), p.scan[:]...)
}

// ResetCC clears continuity-counter state, used at a new source segment.
func (p *Parser) ResetCC() {
	p.cc = map[uint16]uint8{}
	p.ccSeen = map[uint16]bool{}
}

func (p *Parser) feedSection(pkt Packet) {
	if !pkt.HasPayload {
		return
	}
	sec := p.sections[pkt.PID]
	if sec == nil {
		sec = &section{}
		p.sections[pkt.PID] = sec
	}
	payload := pkt.Payload
	if pkt.PUSI {
		pointer := int(payload[0])
		payload = payload[1+pointer:]
		sec.started = true
		sec.buf.Reset()
		sec.remain = 0
	}
	if !sec.started {
		return
	}
	n := len(payload)
	if sec.remain > 0 {
		n = min(n, sec.remain)
	}
	sec.buf.Write(payload[:n])
	sec.remain -= n
	if sec.remain <= 0 && sec.buf.Len() >= 3 {
		total := int(sec.buf.Bytes()[1]&0x0F)<<8 | int(sec.buf.Bytes()[2])
		sec.remain = total + 3 - sec.buf.Len()
		if sec.remain <= 0 {
			p.dispatch(pkt.PID, sec.buf.Bytes())
			sec.started = false
			sec.buf.Reset()
		}
	}
}

func (p *Parser) dispatch(pid uint16, data []byte) {
	if pid == PIDPAT {
		parsePAT(p, data)
	} else {
		parsePMT(p, data)
	}
}
