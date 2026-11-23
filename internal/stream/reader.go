// Package stream provides bounded chunked reads and bounded fan-out with
// explicit backpressure and cancellation propagation.
package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// ErrClosed is returned when a reader or fan-out has been closed.
var ErrClosed = errors.New("stream: closed")

// Chunk is one bounded slice of source bytes with its absolute offset.
type Chunk struct {
	Offset int64
	Index  int
	Data   []byte
}

// Len returns the payload length.
func (c Chunk) Len() int { return len(c.Data) }

// Options bounds memory use.
type Options struct {
	// ChunkBytes is the maximum payload of one produced chunk.
	ChunkBytes int
	// Queue is the number of chunks the producer may buffer ahead of the
	// consumer. Total live chunks never exceed Queue+1.
	Queue int
}

// WithDefaults applies conservative defaults for zero fields.
func (o Options) WithDefaults() Options {
	if o.ChunkBytes <= 0 {
		o.ChunkBytes = 64 << 10
	}
	if o.Queue <= 0 {
		o.Queue = 4
	}
	return o
}

// Validate rejects unusable bounds.
func (o Options) Validate() error {
	if o.ChunkBytes <= 0 {
		return errors.New("stream: chunk bytes must be positive")
	}
	if o.Queue <= 0 {
		return errors.New("stream: queue depth must be positive")
	}
	return nil
}

// Reader streams a source as bounded chunks. The producer goroutine reads at
// most Queue chunks ahead of the consumer; a slow consumer therefore pauses
// the producer rather than growing memory.
type Reader struct {
	src    io.Reader
	opts   Options
	chunks chan Chunk
	done   chan struct{}

	closeOnce sync.Once
	closeErr  error
	producer  sync.WaitGroup

	peakQueue atomic.Int64
	produced  atomic.Int64
	bytesRead atomic.Int64
	liveBytes atomic.Int64
	peakBytes atomic.Int64
	exhausted atomic.Bool
	errMu     sync.Mutex
	readErr   error
}

// Open starts a bounded reader over src.
func Open(src io.Reader, opts Options) (*Reader, error) {
	opts = opts.WithDefaults()
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if src == nil {
		return nil, errors.New("stream: nil source")
	}
	r := &Reader{
		src:    src,
		opts:   opts,
		chunks: make(chan Chunk, opts.Queue),
		done:   make(chan struct{}),
	}
	r.producer.Add(1)
	go r.run()
	return r, nil
}

func (r *Reader) run() {
	defer r.producer.Done()
	defer close(r.chunks)
	buf := make([]byte, r.opts.ChunkBytes)
	index := 0
	var offset int64
	for {
		n, err := io.ReadFull(r.src, buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			ch := Chunk{Offset: offset, Index: index, Data: data}
			r.noteQueue(len(r.chunks) + 1)
			r.noteBytes(r.liveBytes.Add(int64(n)))
			select {
			case r.chunks <- ch:
			case <-r.done:
				r.setReadErr(ErrClosed)
				return
			}
			offset += int64(n)
			index++
			r.produced.Add(1)
			r.bytesRead.Add(int64(n))
		}
		if err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				// A short final read is normal for non-aligned sources.
				r.exhausted.Store(true)
				return
			}
			if !errors.Is(err, io.EOF) {
				r.setReadErr(err)
			} else {
				r.exhausted.Store(true)
			}
			return
		}
	}
}

func (r *Reader) setReadErr(err error) {
	r.errMu.Lock()
	defer r.errMu.Unlock()
	if r.readErr == nil {
		r.readErr = err
	}
}

// Err reports the producer-side error, if any.
func (r *Reader) Err() error {
	r.errMu.Lock()
	defer r.errMu.Unlock()
	return r.readErr
}

func (r *Reader) noteQueue(depth int) {
	for {
		cur := r.peakQueue.Load()
		if int64(depth) <= cur || r.peakQueue.CompareAndSwap(cur, int64(depth)) {
			return
		}
	}
}

// Next returns the next chunk. It blocks until a chunk is available, ctx is
// done, or the reader is closed/exhausted.
func (r *Reader) Next(ctx context.Context) (Chunk, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return Chunk{}, ctx.Err()
	case <-r.done:
		return Chunk{}, ErrClosed
	case ch, ok := <-r.chunks:
		if !ok {
			if err := r.Err(); err != nil {
				return Chunk{}, err
			}
			return Chunk{}, io.EOF
		}
		r.release(int64(len(ch.Data)))
		return ch, nil
	}
}

// release accounts for a chunk leaving the reader's buffer.
func (r *Reader) release(n int64) { r.liveBytes.Add(-n) }

func (r *Reader) noteBytes(v int64) {
	for {
		cur := r.peakBytes.Load()
		if v <= cur || r.peakBytes.CompareAndSwap(cur, v) {
			return
		}
	}
}

// Close stops the producer and releases the queue. It is idempotent and waits
// for the producer goroutine to exit.
func (r *Reader) Close() error {
	r.closeOnce.Do(func() {
		close(r.done)
		r.producer.Wait()
	})
	return r.closeErr
}

// Stats is a point-in-time snapshot of reader accounting.
type Stats struct {
	Chunks      int64
	Bytes       int64
	PeakQueued  int64
	PeakBytes   int64
	Exhausted   bool
	MaxChunkCap int
}

// Stats returns a snapshot. PeakQueued counts the highest number of chunks
// simultaneously buffered or in flight, including the one being produced.
func (r *Reader) Stats() Stats {
	return Stats{
		Chunks:      r.produced.Load(),
		Bytes:       r.bytesRead.Load(),
		PeakQueued:  r.peakQueue.Load(),
		PeakBytes:   r.peakBytes.Load(),
		Exhausted:   r.exhausted.Load(),
		MaxChunkCap: r.opts.Queue + 1,
	}
}

// Bounds returns the configured memory bound in bytes: one production block
// plus the queue allowance.
func (r *Reader) Bounds() int64 {
	return int64(r.opts.Queue+1) * int64(r.opts.ChunkBytes)
}

// String renders the bounds for audit output.
func (r *Reader) String() string {
	return fmt.Sprintf("reader{chunk=%d queue=%d bound=%d}", r.opts.ChunkBytes, r.opts.Queue, r.Bounds())
}
