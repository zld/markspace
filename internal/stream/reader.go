// Package stream provides bounded, chunked reading of local elementary
// stream files. Reads never grow beyond configured caps so a slow consumer
// cannot make the process footprint proportional to the input size.
package stream

import (
	"context"
	"io"

	mediaerr "mediacheck/internal/errors"
)

// Hard caps protect the local kernel from pathological configuration.
const (
	MaxChunkBytes = 8 << 20 // 8 MiB per chunk
	MaxQueueLen   = 1024
	MinChunkBytes = 64
)

// Chunk is one bounded read result. Data is owned by the consumer until the
// next Receive call; it must not be retained beyond that point.
type Chunk struct {
	Seq   int
	Start int64 // absolute byte offset of Data[0] in the source
	Data  []byte
}

// ReaderConfig configures a BoundedReader.
type ReaderConfig struct {
	ChunkBytes int
	QueueLen   int
}

// Validate clamps and checks the configuration against the hard caps.
func (c ReaderConfig) Validate() error {
	if c.ChunkBytes < MinChunkBytes || c.ChunkBytes > MaxChunkBytes {
		return mediaerr.NewCoded(mediaerr.StageStream, "", "stream.config",
			"chunk-bytes %d outside [%d,%d]", c.ChunkBytes, MinChunkBytes, MaxChunkBytes)
	}
	if c.QueueLen < 1 || c.QueueLen > MaxQueueLen {
		return mediaerr.NewCoded(mediaerr.StageStream, "", "stream.config",
			"queue %d outside [1,%d]", c.QueueLen, MaxQueueLen)
	}
	return nil
}

// BoundedReader pulls fixed-size chunks from an io.Reader through a bounded
// queue. When the consumer is slow, the producer goroutine blocks on Enqueue,
// which applies natural back-pressure and keeps peak memory measurable.
type BoundedReader struct {
	cfg    ReaderConfig
	source io.Reader
	queue  *Queue[Chunk]
}

// NewBoundedReader validates the config and wraps source.
func NewBoundedReader(source io.Reader, cfg ReaderConfig) (*BoundedReader, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &BoundedReader{
		cfg:    cfg,
		source: source,
		queue:  NewQueue[Chunk](cfg.QueueLen, cfg.ChunkBytes),
	}, nil
}

// Run reads until EOF or an error and closes the chunk queue. It is intended
// to run in its own goroutine while another goroutine calls Receive.
//
// Every chunk gets its own backing array: a reusable buffer would race with
// consumers still scanning the previous chunk. Back-pressure keeps the number
// of live copies bounded by the queue capacity, so total buffered bytes stay
// bounded by chunk-bytes x queue-len.
func (r *BoundedReader) Run(ctx context.Context) error {
	defer r.queue.Close()
	var offset int64
	seq := 0
	for {
		buf := make([]byte, r.cfg.ChunkBytes)
		n, err := io.ReadFull(r.source, buf)
		if n > 0 {
			chunk := Chunk{Seq: seq, Start: offset, Data: buf[:n]}
			if enqErr := r.queue.Enqueue(ctx, chunk); enqErr != nil {
				return enqErr // cancelled or closed: stop producing, drop rest
			}
			offset += int64(n)
			seq++
		}
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			return nil // final partial chunk already delivered above
		}
		if err != nil {
			return mediaerr.Wrap(err, mediaerr.StageStream, "", "source read failed")
		}
	}
}

// Receive returns the next chunk, or false when the queue is drained or ctx
// is cancelled. Cancel unblocks a producer blocked in Enqueue as well.
func (r *BoundedReader) Receive(ctx context.Context) (Chunk, bool) {
	return r.queue.Dequeue(ctx)
}

// Stats reports queue pressure observed so far.
func (r *BoundedReader) Stats() Stats { return r.queue.Stats() }

// Fail closes the queue with an error, used when downstream writing fails and
// every goroutine must exit.
func (r *BoundedReader) Fail(err error) { r.queue.Fail(err) }
