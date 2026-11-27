// Package ingest performs bounded, cancelable reads of raw codec bitstreams
// and key-frame indexes. Memory use is governed solely by the configured
// chunk size and queue depth; it never grows with the total asset length.
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync/atomic"

	"mediad/internal/model"
)

// DefaultChunkSize is the fixed read buffer size (64 KiB) required by the
// pipeline specification.
const DefaultChunkSize = 64 * 1024

// chunkBuf is one pooled fixed-size buffer. At most QueueDepth+1 of these
// exist for a stream, which gives a hard, deterministic memory ceiling.
type chunkBuf struct {
	data  []byte
	inUse atomic.Bool
}

// Chunk is one fixed-size buffer of stream bytes handed to a consumer.
// The consumer must call Release exactly once when it is done with Data;
// Release returns the backing buffer to the pool. Retaining Data after
// Release is a programming error.
type Chunk struct {
	// Data is a prefix of the pooled buffer; its length may be smaller than
	// ChunkSize for the final chunk.
	Data []byte
	// Offset is the absolute byte offset of Data[0] within the source file.
	Offset int64
	buf    *chunkBuf
	free   chan<- *chunkBuf
}

// Release returns the backing buffer to the pool. It is idempotent.
func (c Chunk) Release() {
	if c.buf == nil {
		return
	}
	if c.buf.inUse.CompareAndSwap(true, false) {
		c.free <- c.buf
	}
}

// ReaderConfig controls bounded reading.
type ReaderConfig struct {
	// ChunkSize is the fixed buffer size in bytes. Zero selects
	// DefaultChunkSize.
	ChunkSize int
	// QueueDepth bounds the number of chunks buffered between producer and
	// consumer. The peak in-flight memory is therefore
	// (QueueDepth+1)*ChunkSize bytes regardless of asset length.
	QueueDepth int
}

func (c ReaderConfig) normalize() ReaderConfig {
	if c.ChunkSize <= 0 {
		c.ChunkSize = DefaultChunkSize
	}
	if c.QueueDepth <= 0 {
		c.QueueDepth = 4
	}
	return c
}

// PeakBytes returns the configured upper bound on bytes held in flight
// (queued chunks plus the single in-progress read buffer).
func (c ReaderConfig) PeakBytes() int {
	c = c.normalize()
	return (c.QueueDepth + 1) * c.ChunkSize
}

// Stats reports observed buffering behavior. Fields are safe to read after
// the producing goroutine has terminated.
type Stats struct {
	// PeakInFlight is the high-water mark of allocated bytes simultaneously
	// outstanding (pooled buffers held by the queue, the producer or a
	// consumer). It must never exceed ReaderConfig.PeakBytes().
	PeakInFlight int64
	BytesRead    int64
	Chunks       int64
}

// StreamChunks reads r in fixed-size chunks and pushes them onto a bounded
// channel backed by a preallocated buffer pool of QueueDepth+1 buffers.
//
// Back-pressure: when the channel is full the producer blocks on send, and
// when every pooled buffer is outstanding it blocks acquiring the next one,
// so read throughput is governed by the consumer. Cancellation: when ctx is
// canceled the goroutine stops, closes the channel and returns; any buffers
// still held by consumers are returned to the pool on Release.
// The returned channel is always closed exactly once.
func StreamChunks(ctx context.Context, r io.Reader, cfg ReaderConfig) (<-chan Chunk, *Stats) {
	cfg = cfg.normalize()
	out := make(chan Chunk, cfg.QueueDepth)
	free := make(chan *chunkBuf, cfg.QueueDepth+1)
	for i := 0; i < cfg.QueueDepth+1; i++ {
		free <- &chunkBuf{data: make([]byte, cfg.ChunkSize)}
	}
	var stats Stats
	// The whole pool exists for the lifetime of the stream: QueueDepth chunks
	// may sit in the channel while one additional buffer is being read into.
	// No other buffer is ever allocated, so this is both the actual and the
	// configured high-water mark.
	stats.PeakInFlight = int64(cfg.QueueDepth+1) * int64(cfg.ChunkSize)

	go func() {
		defer close(out)
		var offset int64
		for {
			var b *chunkBuf
			select {
			case b = <-free:
			case <-ctx.Done():
				return
			}
			b.inUse.Store(true)

			n, err := r.Read(b.data)
			if n > 0 {
				ch := Chunk{Data: b.data[:n:n], Offset: offset, buf: b, free: free}
				select {
				case out <- ch:
				case <-ctx.Done():
					// Nobody will receive this chunk; recycle its buffer.
					if b.inUse.CompareAndSwap(true, false) {
						free <- b
					}
					return
				}
				offset += int64(n)
				atomic.AddInt64(&stats.BytesRead, int64(n))
				atomic.AddInt64(&stats.Chunks, 1)
			} else {
				// Empty read: nothing was handed out, recycle immediately.
				if b.inUse.CompareAndSwap(true, false) {
					free <- b
				}
			}
			if err != nil {
				return // io.EOF or a real read error both terminate the stream
			}
			if ctx.Err() != nil {
				return
			}
		}
	}()
	return out, &stats
}

// HashFile computes the SHA-256 digest of a file using the fixed-size chunk
// reader, so hashing never maps the whole file into memory. It also returns
// the total byte length and read statistics.
func HashFile(ctx context.Context, path string, cfg ReaderConfig) (sum [32]byte, size int64, stats *Stats, err error) {
	f, err := os.Open(path)
	if err != nil {
		return sum, 0, nil, err
	}
	defer f.Close()

	h := sha256.New()
	chunks, stats := StreamChunks(ctx, f, cfg)
	for ch := range chunks {
		_, _ = h.Write(ch.Data) // sha256.Writer never errors
		size += int64(len(ch.Data))
		ch.Release()
	}
	if ctx.Err() != nil {
		return sum, 0, stats, ctx.Err()
	}
	copy(sum[:], h.Sum(nil))
	return sum, size, stats, nil
}

// HexDigest returns the lowercase hex encoding of a SHA-256 digest.
func HexDigest(sum [32]byte) string { return hex.EncodeToString(sum[:]) }

// Guard checks observed peak buffering against the configured ceiling and
// returns a structured error if it was exceeded.
func Guard(stats *Stats, cfg ReaderConfig) error {
	if stats.PeakInFlight > int64(cfg.PeakBytes()) {
		return model.New(model.ErrBufferLimit, "", -1,
			"peak buffered bytes %d exceed configured ceiling %d",
			stats.PeakInFlight, cfg.PeakBytes())
	}
	return nil
}
