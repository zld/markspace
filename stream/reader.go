// Package stream provides bounded, cancellation-aware primitives for moving
// media bytes from producers to consumers without ever buffering a whole
// stream in memory.
package stream

import (
	"context"
	"errors"
	"io"
	"sync"

	"mediamux/media"
)

// ErrCanceled is returned when the context ends mid-read.
var ErrCanceled = errors.New("stream: read canceled")

// ChunkReader reads an underlying stream in fixed-size chunks bounded by a
// memory limit. On cancellation it closes the underlying reader so a blocked
// Read unblocks instead of leaking a goroutine.
type ChunkReader struct {
	ctx    context.Context
	src    io.ReadCloser
	buf    []byte
	chunk  int
	limit  int64
	read   int64
	closed bool
	mu     sync.Mutex
}

// NewChunkReader builds a reader whose per-read buffer never exceeds limit
// bytes. A limit smaller than one chunk still yields one chunk no larger than
// the limit.
func NewChunkReader(ctx context.Context, src io.ReadCloser, limit int64) (*ChunkReader, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if src == nil {
		return nil, media.NewError(media.CodeInput, media.PhaseRead, "nil source reader")
	}
	if limit <= 0 {
		return nil, media.NewError(media.CodeInput, media.PhaseRead, "memory limit must be positive").AtOffset(0)
	}
	if limit > 64<<20 {
		limit = 64 << 20
	}
	chunk := int(limit)
	if chunk > 64<<10 {
		chunk = 64 << 10
	}
	return &ChunkReader{ctx: ctx, src: src, buf: make([]byte, chunk), chunk: chunk, limit: limit}, nil
}

// ChunkSize is the size of each read buffer.
func (r *ChunkReader) ChunkSize() int { return r.chunk }

// Read fills p from the source. It checks cancellation before every read and
// closes the source when the context is done.
func (r *ChunkReader) Read(p []byte) (int, error) {
	if err := r.checkCanceled(); err != nil {
		return 0, err
	}
	if len(p) > r.chunk {
		p = p[:r.chunk]
	}
	n, err := r.src.Read(p)
	r.read += int64(n)
	if err != nil && r.ctx.Err() != nil {
		return n, r.cancelErr()
	}
	return n, err
}

// NextChunk returns the next non-empty chunk of bounded size. The returned
// slice is only valid until the next call to NextChunk.
func (r *ChunkReader) NextChunk() ([]byte, error) {
	for {
		n, err := r.Read(r.buf)
		if n > 0 {
			return r.buf[:n], nil
		}
		if err == io.EOF {
			return nil, io.EOF
		}
		if err != nil {
			return nil, err
		}
	}
}

// Limit is the configured memory ceiling for this reader.
func (r *ChunkReader) Limit() int64 { return r.limit }

// AllocatedBytes is the size of the single reusable buffer this reader owns.
// It is never larger than the configured limit.
func (r *ChunkReader) AllocatedBytes() int64 { return int64(r.chunk) }

// BytesRead is the total number of bytes returned so far.
func (r *ChunkReader) BytesRead() int64 { return r.read }

// Close closes the source exactly once.
func (r *ChunkReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.src.Close()
}

// checkCanceled converts a done context into a structured error and closes
// the source so any in-flight Read can return.
func (r *ChunkReader) checkCanceled() error {
	select {
	case <-r.ctx.Done():
		_ = r.Close()
		return r.cancelErr()
	default:
		return nil
	}
}

func (r *ChunkReader) cancelErr() error {
	cause := r.ctx.Err()
	if cause == nil {
		cause = context.Canceled
	}
	return media.NewError(media.CodeCanceled, media.PhaseRead, "read canceled").
		AtOffset(r.read).Because(cause)
}

// Buffered reads src entirely into buf, never allocating beyond len(buf). It
// returns io.ErrShortBuffer when the input exceeds the supplied capacity, so
// callers cannot silently grow past their ceiling.
func Buffered(ctx context.Context, src io.Reader, buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, media.NewError(media.CodeInput, media.PhaseRead, "zero buffer").AtOffset(0)
	}
	cr, err := NewChunkReader(ctx, io.NopCloser(src), int64(len(buf)))
	if err != nil {
		return 0, err
	}
	defer cr.Close()
	total := 0
	for {
		n, err := cr.Read(buf[total:])
		total += n
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
		if total == len(buf) {
			// Probe for one more byte to distinguish exact fit from overflow.
			var one [1]byte
			if n, err := cr.Read(one[:]); n > 0 {
				return total, io.ErrShortBuffer
			} else if err != nil && err != io.EOF {
				return total, err
			}
			return total, nil
		}
	}
}
