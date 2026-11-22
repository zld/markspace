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

// Reader splits a byte stream into TS packets.
type Reader struct {
	r        io.Reader
	tolerant bool
	stats    Stats
	pending  []byte // bytes kept while resyncing
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
		chunk := make([]byte, 0, PacketSize)
		chunk = append(chunk, r.pending...)
		r.pending = nil
		tmp := make([]byte, PacketSize-len(chunk))
		n, err := io.ReadFull(r.r, tmp)
		chunk = append(chunk, tmp[:n]...)
		if err != nil {
			if len(chunk) == 0 {
				return nil, io.EOF
			}
			r.stats.TruncatedTailBytes += len(chunk)
			if !r.tolerant {
				return nil, ErrCorrupt
			}
			return nil, io.EOF
		}
		if chunk[0] != SyncByte {
			r.stats.SyncErrors++
			if !r.tolerant {
				return nil, ErrCorrupt
			}
			// Resync: keep bytes from the next sync candidate onward.
			if i := bytes.IndexByte(chunk[1:], SyncByte); i >= 0 {
				r.pending = append([]byte(nil), chunk[i+1:]...)
			}
			continue
		}
		p, err := parsePacket(chunk)
		if err != nil {
			r.stats.SyncErrors++
			if !r.tolerant {
				return nil, ErrCorrupt
			}
			continue
		}
		p.Index = r.stats.Packets
		r.stats.Packets++
		if p.TransportError {
			r.stats.TransportErrors++
		}
		return p, nil
	}
}

// parsePacket decodes transport-layer fields of one aligned 188-byte
// packet using go-astits.
func parsePacket(chunk []byte) (*Packet, error) {
	dmx := astits.NewDemuxer(context.Background(), bytes.NewReader(chunk))
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
