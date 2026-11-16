// Package ts parses MPEG-TS packets from a stream: 188-byte framing
// across read buffers, continuity-counter tracking, sync-loss recovery
// and truncation reporting, all with absolute byte offsets.
package ts

import (
	"fmt"
	"io"
)

const (
	// PacketSize is the fixed MPEG-TS packet size in bytes.
	PacketSize = 188
	// SyncByte starts every TS packet.
	SyncByte = 0x47
	// ClockRate is the PTS/DTS clock in Hz.
	ClockRate = 90000

	chunk = PacketSize * 200 // read-ahead granularity
)

// Packet is one parsed 188-byte TS packet.
type Packet struct {
	PID        uint16
	PUSI       bool  // payload unit start indicator
	CC         uint8 // continuity counter
	HasPayload bool
	Payload    []byte // valid until the next Next call
	Raw        []byte // the full 188 bytes, valid until the next Next call
	Offset     int64  // absolute byte offset of the packet in the stream
}

// Anomaly is a non-fatal stream defect noticed while parsing.
type Anomaly struct {
	Offset int64
	Kind   string
	Detail string
}

func (a Anomaly) String() string {
	return fmt.Sprintf("offset %d: %s: %s", a.Offset, a.Kind, a.Detail)
}

// SyncError reports an unrecoverable sync-byte loss at Offset.
type SyncError struct{ Offset int64 }

func (e *SyncError) Error() string {
	return fmt.Sprintf("sync byte error at offset %d", e.Offset)
}

// TruncatedError reports a stream tail shorter than one packet.
type TruncatedError struct {
	Offset int64
	Have   int
}

func (e *TruncatedError) Error() string {
	return fmt.Sprintf("truncated packet at offset %d: %d of %d bytes", e.Offset, e.Have, PacketSize)
}

// Reader streams TS packets without buffering the whole input.
type Reader struct {
	r         io.Reader
	buf       []byte // unparsed bytes only
	base      int64  // absolute offset of buf[0]
	cc        map[uint16]int
	anomalies []Anomaly
	eof       bool
}

// NewReader wraps r with a streaming TS packet reader.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: r, buf: make([]byte, 0, chunk), cc: make(map[uint16]int)}
}

// Anomalies returns the defects collected so far.
func (rd *Reader) Anomalies() []Anomaly { return rd.anomalies }

// fill buffers at least need bytes unless EOF is reached first.
func (rd *Reader) fill(need int) error {
	var tmp [chunk]byte
	for len(rd.buf) < need && !rd.eof {
		n, err := rd.r.Read(tmp[:])
		rd.buf = append(rd.buf, tmp[:n]...)
		if err == io.EOF {
			rd.eof = true
		} else if err != nil {
			return err
		}
	}
	return nil
}

// Next returns the next packet, or io.EOF at a clean packet boundary.
func (rd *Reader) Next() (*Packet, error) {
	for {
		if err := rd.fill(PacketSize); err != nil {
			return nil, err
		}
		if len(rd.buf) == 0 {
			return nil, io.EOF
		}
		if len(rd.buf) < PacketSize {
			off, have := rd.base, len(rd.buf)
			rd.base += int64(have)
			rd.buf = rd.buf[have:]
			return nil, &TruncatedError{Offset: off, Have: have}
		}
		if rd.buf[0] == SyncByte {
			break
		}
		if err := rd.resync(); err != nil {
			return nil, err
		}
	}
	raw := rd.buf[:PacketSize]
	p := parsePacket(raw, rd.base)
	rd.checkCC(p)
	rd.buf = rd.buf[PacketSize:]
	rd.base += PacketSize
	return p, nil
}

// resync scans for a 0x47 confirmed by another one 188 bytes later.
func (rd *Reader) resync() error {
	for {
		for i := 1; i+PacketSize < len(rd.buf); i++ {
			if rd.buf[i] == SyncByte && rd.buf[i+PacketSize] == SyncByte {
				rd.anomalies = append(rd.anomalies, Anomaly{
					Offset: rd.base, Kind: "resync",
					Detail: fmt.Sprintf("skipped %d stray bytes", i),
				})
				rd.buf = rd.buf[i:]
				rd.base += int64(i)
				return nil
			}
		}
		if rd.eof {
			return &SyncError{Offset: rd.base}
		}
		if err := rd.fill(len(rd.buf) + chunk); err != nil {
			return err
		}
	}
}

func parsePacket(raw []byte, off int64) *Packet {
	p := &Packet{Raw: raw, Offset: off}
	p.PUSI = raw[1]&0x40 != 0
	p.PID = uint16(raw[1]&0x1F)<<8 | uint16(raw[2])
	afc := (raw[3] >> 4) & 0x3
	p.CC = raw[3] & 0x0F
	p.HasPayload = afc == 1 || afc == 3
	pos := 4
	if afc == 2 || afc == 3 {
		pos += 1 + int(raw[4])
	}
	if p.HasPayload && pos <= PacketSize {
		p.Payload = raw[pos:]
	} else {
		p.HasPayload = false
	}
	return p
}

// checkCC verifies the continuity counter of packets carrying payload.
func (rd *Reader) checkCC(p *Packet) {
	if !p.HasPayload || p.PID == 0x1FFF {
		return
	}
	if last, ok := rd.cc[p.PID]; ok {
		if want := (last + 1) & 0x0F; int(p.CC) != want {
			rd.anomalies = append(rd.anomalies, Anomaly{
				Offset: p.Offset, Kind: "cc_jump",
				Detail: fmt.Sprintf("pid 0x%04x: got cc=%d want %d", p.PID, p.CC, want),
			})
		}
	}
	rd.cc[p.PID] = int(p.CC)
}
