// Package ts provides a streaming MPEG-TS packet reader built on go-astits.
// It owns framing concerns (188-byte sync, leading offset, truncated tail)
// and exposes the transport fields needed by the PSI and counter stages.
package ts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/asticode/go-astits"
)

// PacketSize is the fixed MPEG-TS packet size used by this tool.
const PacketSize = 188

// Packet is one synchronized transport-stream packet.
type Packet struct {
	Index              int64
	Offset             int64 // absolute byte offset in the input file
	PID                uint16
	ContinuityCounter  uint8
	HasPayload         bool
	HasAdaptationField bool
	PayloadUnitStart   bool
	TransportError     bool
	Discontinuity      bool
	Payload            []byte
}

// SyncError reports a packet that did not begin with the 0x47 sync byte.
type SyncError struct {
	Offset int64
	Err    error
}

func (e *SyncError) Error() string {
	return fmt.Sprintf("ts: no sync byte at input offset %d: %v", e.Offset, e.Err)
}

func (e *SyncError) Unwrap() error { return e.Err }

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Reader streams 188-byte packets from a local file.
type Reader struct {
	f       *os.File
	dmx     *astits.Demuxer
	cr      *countingReader
	prefix  int64
	index   int64
	packets int64
	trail   int64
	done    bool
}

// Open opens path for streaming packet reads. offset bytes at the front of the
// file are skipped before the first sync byte is expected.
func Open(path string, offset int64) (*Reader, error) {
	if offset < 0 {
		return nil, fmt.Errorf("ts: leading offset must be non-negative, got %d", offset)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("ts: opening input: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("ts: stat input: %w", err)
	}
	if offset > info.Size() {
		f.Close()
		return nil, fmt.Errorf("ts: leading offset %d is beyond input size %d", offset, info.Size())
	}
	if offset > 0 {
		if _, err = f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return nil, fmt.Errorf("ts: seeking past leading offset: %w", err)
		}
	}
	cr := &countingReader{r: f}
	return &Reader{
		f:      f,
		cr:     cr,
		prefix: offset,
		dmx:    astits.NewDemuxer(context.Background(), cr, astits.DemuxerOptPacketSize(PacketSize)),
	}, nil
}

// Next returns the next packet, or io.EOF at the end of the stream. A final
// short fragment is recorded and reported via TrailingBytes.
func (r *Reader) Next() (*Packet, error) {
	if r.done {
		return nil, io.EOF
	}
	p, err := r.dmx.NextPacket()
	if err != nil {
		if errors.Is(err, astits.ErrNoMorePackets) {
			r.done = true
			consumed := r.packets * PacketSize
			if r.cr.n > consumed {
				r.trail = r.cr.n - consumed
			}
			return nil, io.EOF
		}
		r.done = true
		if strings.Contains(err.Error(), astits.ErrPacketMustStartWithASyncByte.Error()) {
			at := r.prefix + r.cr.n - PacketSize
			if at < r.prefix {
				at = r.prefix
			}
			return nil, &SyncError{Offset: at, Err: err}
		}
		return nil, fmt.Errorf("ts: reading packet %d: %w", r.index, err)
	}
	pkt := &Packet{
		Index:              r.index,
		Offset:             r.prefix + r.index*PacketSize,
		PID:                p.Header.PID,
		ContinuityCounter:  p.Header.ContinuityCounter,
		HasPayload:         p.Header.HasPayload,
		HasAdaptationField: p.Header.HasAdaptationField,
		PayloadUnitStart:   p.Header.PayloadUnitStartIndicator,
		TransportError:     p.Header.TransportErrorIndicator,
		Payload:            p.Payload,
	}
	if p.AdaptationField != nil {
		pkt.Discontinuity = p.AdaptationField.DiscontinuityIndicator
	}
	r.index++
	r.packets++
	return pkt, nil
}

// Close releases the underlying file.
func (r *Reader) Close() error {
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// Packets returns the number of complete packets read so far.
func (r *Reader) Packets() int64 { return r.packets }

// TrailingBytes returns the size of the final incomplete packet fragment.
func (r *Reader) TrailingBytes() int64 { return r.trail }

// TrailingPackets returns 1 when a final incomplete fragment exists, else 0.
func (r *Reader) TrailingPackets() int64 {
	if r.trail == 0 {
		return 0
	}
	return 1
}

// PrefixOffset returns the number of bytes skipped before the first packet.
func (r *Reader) PrefixOffset() int64 { return r.prefix }

// BytesRead returns all bytes consumed from the file, including prefix bytes.
func (r *Reader) BytesRead() int64 { return r.prefix + r.cr.n }
