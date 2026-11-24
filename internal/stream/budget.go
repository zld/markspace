package stream

import (
	"context"
	"sync"

	"medpipe/internal/clock"
)

// Budget is a byte-count memory ceiling. Acquired bytes cover both queued
// envelopes and the envelope currently being processed; Ack releases them.
type Budget struct {
	mu    sync.Mutex
	cond  *sync.Cond
	max   int64
	used  int64
	peak  int64
	waits int64
}

func NewBudget(maxBytes int64) *Budget {
	b := &Budget{max: maxBytes}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// Acquire blocks until n bytes are available or ctx is done. It never grants
// an allocation that would make Used exceed Max.
func (b *Budget) Acquire(ctx context.Context, n int64) error {
	if n <= 0 {
		return clock.Errorf(clock.CodeInvalidPacket, &clock.Location{ByteOffset: -1}, "queue item size must be positive: %d", n)
	}
	if n > b.max {
		return clock.Errorf(clock.CodePacketTooLarge, &clock.Location{ByteOffset: -1},
			"packet size %d exceeds queue byte limit %d", n, b.max)
	}

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			b.cond.Broadcast()
		case <-done:
		}
	}()
	defer close(done)

	b.mu.Lock()
	defer b.mu.Unlock()
	waited := false
	for b.used+n > b.max {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !waited {
			b.waits++
			waited = true
		}
		b.cond.Wait()
	}
	b.used += n
	if b.used > b.peak {
		b.peak = b.used
	}
	return nil
}

// TryAcquire is the non-blocking form used by tests and diagnostics.
func (b *Budget) TryAcquire(n int64) bool {
	if n <= 0 || n > b.max {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used+n > b.max {
		return false
	}
	b.used += n
	if b.used > b.peak {
		b.peak = b.used
	}
	return true
}

func (b *Budget) Release(n int64) {
	if n <= 0 {
		return
	}
	b.mu.Lock()
	b.used -= n
	if b.used < 0 {
		b.used = 0
	}
	b.cond.Broadcast()
	b.mu.Unlock()
}

func (b *Budget) Used() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

func (b *Budget) Max() int64 { return b.max }

func (b *Budget) Peak() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.peak
}

func (b *Budget) WaitCount() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.waits
}
