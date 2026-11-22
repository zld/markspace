// Package transport reads 188-byte MPEG-TS packets from an input stream
// and decodes transport-layer fields with go-astits.
package transport

import (
	"bytes"
	"context"
	"errors"
	"io"

	astits "github.com/asticode/go-astits"
)

const (
	// PacketSize is the standard MPEG-TS packet size in bytes.
	PacketSize = 188
	// SyncByte marks the start of every TS packet.
	SyncByte = 0x47
	// NullPID is the padding PID; its continuity counter is undefined.
	NullPID = 0x1FFF
)

// ErrCorrupt reports structural input damage (bad sync byte or truncated
// tail) encountered while not in tolerant mode.
var ErrCorrupt = errors.New("transport: corrupt input structure")

// Packet is one decoded TS packet.
type Packet struct {
	Index                  int
	PID                    uint16
	PayloadUnitStart       bool
	HasPayload             bool
	ContinuityCounter      uint8
	TransportError         bool
	DiscontinuityIndicator bool
	Payload                []byte
}

// Stats counts input-level anomalies observed while reading.
type Stats struct {
	Packets            int
	SyncErrors         int
	TruncatedTailBytes int
	TransportErrors    int
}

// Reader splits a byte stream into TS packets. Only a small sliding
// window is buffered; the media file is never held in memory.
type Reader struct {
	r         io.Reader
	tolerant  bool
	stats     Stats
	buf       []byte
	eof       bool
	resyncing bool
}

// NewReader builds a Reader. In tolerant mode corrupt bytes are skipped
// and counted instead of aborting the read.
func NewReader(r io.Reader, tolerant bool) *Reader {
	return &Reader{r: r, tolerant: tolerant}
}

// Stats returns the anomalies counted so far.
func (r *Reader) Stats() Stats { return r.stats }

// Next returns the next decodable packet, io.EOF at clean end of input,
// or ErrCorrupt on structural damage in strict mode.
func (r *Reader) Next() (*Packet, error) {
	for {
		r.need(PacketSize)
		if len(r.buf) == 0 {
			return nil, io.EOF
		}
		if len(r.buf) < PacketSize { // truncated tail
			r.stats.TruncatedTailBytes += len(r.buf)
			r.buf = nil
			if !r.tolerant {
				return nil, ErrCorrupt
			}
			return nil, io.EOF
		}
		if r.buf[0] != SyncByte {
			if !r.tolerant {
				return nil, ErrCorrupt
			}
			r.markResync()
			i := bytes.IndexByte(r.buf[1:], SyncByte)
			if i < 0 {
				if r.eof {
					r.stats.TruncatedTailBytes += len(r.buf)
					r.buf = nil
					return nil, io.EOF
				}
				r.buf = r.buf[:0]
				continue
			}
			r.buf = r.buf[i+1:]
			continue
		}
		if r.resyncing {
			// A lone 0x47 may be payload data; require the next
			// packet boundary to confirm alignment.
			r.need(2 * PacketSize)
			if len(r.buf) >= 2*PacketSize && r.buf[PacketSize] != SyncByte {
				r.buf = r.buf[1:]
				continue
			}
		}
		chunk := r.buf[:PacketSize]
		r.buf = r.buf[PacketSize:]
		p, err := parsePacket(chunk)
		if err != nil {
			if !r.tolerant {
				return nil, ErrCorrupt
			}
			r.markResync()
			continue
		}
		r.resyncing = false
		p.Index = r.stats.Packets
		r.stats.Packets++
		if p.TransportError {
			r.stats.TransportErrors++
		}
		return p, nil
	}
}

// need buffers at least n bytes unless EOF is reached first.
func (r *Reader) need(n int) {
	for len(r.buf) < n && !r.eof {
		tmp := make([]byte, 8192)
		m, err := r.r.Read(tmp)
		if m > 0 {
			r.buf = append(r.buf, tmp[:m]...)
		}
		if err != nil {
			r.eof = true // io.EOF or a read error both end the stream
		}
	}
}

// markResync counts one sync-loss event per resync episode.
func (r *Reader) markResync() {
	if !r.resyncing {
		r.stats.SyncErrors++
		r.resyncing = true
	}
}

// parsePacket decodes transport-layer fields of one aligned 188-byte
// packet using go-astits.
func parsePacket(chunk []byte) (*Packet, error) {
	dmx := astits.NewDemuxer(context.Background(), bytes.NewReader(chunk),
		astits.DemuxerOptPacketSize(PacketSize))
	ap, err := dmx.NextPacket()
	if err != nil {
		return nil, err
	}
	p := &Packet{
		PID:               ap.Header.PID,
		PayloadUnitStart:  ap.Header.PayloadUnitStartIndicator,
		HasPayload:        ap.Header.HasPayload,
		ContinuityCounter: ap.Header.ContinuityCounter,
		TransportError:    ap.Header.TransportErrorIndicator,
		Payload:           ap.Payload,
	}
	if ap.AdaptationField != nil {
		p.DiscontinuityIndicator = ap.AdaptationField.DiscontinuityIndicator
	}
	return p, nil
}
