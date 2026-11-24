// Package stream performs bounded file reads. A shared Budget caps total
// outstanding bytes; chunk production blocks when the budget is exhausted
// or when the downstream consumer is slow, so memory use is bounded and no
// unbounded buffer queue can accumulate.
package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// ErrOverLimit means a single allocation request is larger than the budget.
var ErrOverLimit = errors.New("stream: requested bytes exceed memory budget")

// Budget is a mutex/cond based byte reservation counter.
type Budget struct {
	max  int64
	mu   sync.Mutex
	cond *sync.Cond
	used int64
	peak int64
}

// NewBudget creates a hard cap of maxBytes concurrently reserved bytes.
func NewBudget(maxBytes int64) *Budget {
	b := &Budget{max: maxBytes}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// Max returns the cap.
func (b *Budget) Max() int64 { return b.max }

// Used returns currently reserved bytes.
func (b *Budget) Used() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

// Peak returns the highest simultaneous reservation observed.
func (b *Budget) Peak() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.peak
}

// TryTake reserves n bytes without blocking.
func (b *Budget) TryTake(n int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > b.max || b.used+n > b.max {
		return false
	}
	b.used += n
	if b.used > b.peak {
		b.peak = b.used
	}
	return true
}

// Take reserves n bytes, blocking while the cap would be exceeded. It returns
// ErrOverLimit immediately if n can never fit, and honors ctx cancellation.
func (b *Budget) Take(ctx context.Context, n int64) error {
	if n > b.max {
		return fmt.Errorf("%w: want=%d max=%d", ErrOverLimit, n, b.max)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.used+n > b.max {
		if err := ctx.Err(); err != nil {
			return err
		}
		stop := context.AfterFunc(ctx, func() {
			b.mu.Lock()
			b.cond.Broadcast()
			b.mu.Unlock()
		})
		b.cond.Wait()
		stop()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b.used += n
	if b.used > b.peak {
		b.peak = b.used
	}
	return nil
}

// Give releases n reserved bytes and wakes waiting producers.
func (b *Budget) Give(n int64) {
	b.mu.Lock()
	if n > b.used {
		b.used = 0
	} else {
		b.used -= n
	}
	b.cond.Broadcast()
	b.mu.Unlock()
}

// DefaultChunkSize is 64 KiB.
const DefaultChunkSize = 64 << 10

// Chunk is one bounded read unit. Release must be called exactly once when
// the consumer is done with its bytes.
type Chunk struct {
	Seq      int64
	Data     []byte
	reserved int64
	budget   *Budget
	once     sync.Once
}

// Release returns the chunk's reservation to the budget.
func (c *Chunk) Release() {
	c.once.Do(func() { c.budget.Give(c.reserved) })
}

// StreamFile reads path in fixed chunks, enforcing the budget and applying
// backpressure through the returned channel. The channel is unbuffered: a
// slow consumer blocks the producer directly. Any producer error is sent on
// errs (at most one value) and both channels are closed.
func StreamFile(ctx context.Context, path string, budget *Budget, chunkSize int) (<-chan *Chunk, <-chan error) {
	out := make(chan *Chunk)
	errs := make(chan error, 1)
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}

	go func() {
		defer close(errs)
		defer close(out)
		f, err := os.Open(path)
		if err != nil {
			errs <- err
			return
		}
		defer f.Close()

		var seq int64
		for {
			select {
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			default:
			}
			if err := budget.Take(ctx, int64(chunkSize)); err != nil {
				errs <- err
				return
			}
			buf := make([]byte, chunkSize)
			n, rerr := io.ReadFull(f, buf)
			if n == 0 {
				budget.Give(int64(chunkSize))
				if errors.Is(rerr, io.EOF) {
					return
				}
				errs <- rerr
				return
			}
			c := &Chunk{Seq: seq, Data: buf[:n], reserved: int64(chunkSize), budget: budget}
			seq++
			select {
			case out <- c:
				// ownership transferred; receiver calls Release.
			case <-ctx.Done():
				c.Release()
				errs <- ctx.Err()
				return
			}
			if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
				return
			}
			if rerr != nil {
				errs <- rerr
				return
			}
		}
	}()

	return out, errs
}

// Drain fully consumes a stream, invoking handle for each chunk and
// releasing chunks automatically. It returns the total byte count.
func Drain(ctx context.Context, path string, budget *Budget, chunkSize int, handle func(seq int64, p []byte) error) (int64, error) {
	chunks, errs := StreamFile(ctx, path, budget, chunkSize)
	var total int64
	var handleErr error
	for c := range chunks {
		if handleErr == nil {
			if err := handle(c.Seq, c.Data); err != nil {
				handleErr = err
			}
		}
		c.Release()
		total += int64(len(c.Data))
	}
	if handleErr != nil {
		return total, handleErr
	}
	if err := <-errs; err != nil {
		return total, err
	}
	return total, nil
}
