// Package fanout distributes repaired blocks to several bitrate
// representations with independent recovery state.
//
// Every layer owns a fixed 4 MiB byte budget and its own RTP reorder window.
// A slow consumer is throttled by backpressure on the publisher: blocks are
// never silently discarded to "catch up", and the publisher never blocks
// indefinitely. A consumer that walks away cancels only its own layer, so the
// remaining representations keep running unaffected.
package fanout

import (
	"context"
	"sync"
	"time"

	"mediad/internal/media"
	"mediad/internal/rtp"
)

// pendingBlock is a block tagged with the sequence it was offered under.
type pendingBlock struct {
	seq   uint16
	block media.Block
}

// Layer is one representation sink with independent recovery state.
type Layer struct {
	opts LayerOptions

	mu    sync.Mutex
	cond  *sync.Cond
	queue []media.Block
	bytes int

	closed   bool
	canceled bool
	stats    LayerStats

	// Transport state.
	delayed *pendingBlock
	blocks  map[uint16]media.Block
	cursor  *rtp.Cursor
}

// NewLayer creates a layer. It is passive: the publisher drives it through
// Feed and the consumer drains it through Next.
func NewLayer(opts LayerOptions) *Layer {
	o := opts.withDefaults()
	l := &Layer{
		opts:   o,
		cursor: rtp.NewCursor(o.Transp.Window),
		blocks: make(map[uint16]media.Block),
	}
	l.stats.Name = o.Name
	l.cond = sync.NewCond(&l.mu)
	return l
}

// Name returns the layer name.
func (l *Layer) Name() string { return l.opts.Name }

// Cancel stops only this layer; buffered blocks are discarded and both Feed
// and Next return immediately.
func (l *Layer) Cancel() {
	l.mu.Lock()
	l.canceled = true
	l.closed = true
	l.queue = nil
	l.bytes = 0
	l.mu.Unlock()
	l.cond.Broadcast()
}

// Close ends the layer normally; buffered blocks remain available to Next.
func (l *Layer) Close() {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	l.cond.Broadcast()
}

// Canceled reports whether the layer was canceled.
func (l *Layer) Canceled() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.canceled
}

// Stats returns a snapshot.
func (l *Layer) Stats() LayerStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.stats
	s.BufferedBytes = l.bytes
	s.Canceled = l.canceled
	s.Closed = l.closed
	return s
}

// Next returns the next block in sequence order. ok is false once the layer
// is closed and drained, or canceled.
func (l *Layer) Next(ctx context.Context) (blk media.Block, ok bool) {
	l.mu.Lock()
	for len(l.queue) == 0 && !l.closed && ctx.Err() == nil {
		l.cond.Wait()
	}
	if len(l.queue) == 0 || l.canceled {
		l.mu.Unlock()
		return media.Block{}, false
	}
	b := l.queue[0]
	l.queue = l.queue[1:]
	l.bytes -= b.Size()
	l.stats.Delivered++
	l.mu.Unlock()
	l.cond.Broadcast()

	if l.opts.Slow > 0 {
		t := time.NewTimer(l.opts.Slow)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return media.Block{}, false
		}
	}
	return b, true
}

// errLayerCanceled is the internal sentinel for a canceled layer.
var errLayerCanceled = &layerCancelError{}

type layerCancelError struct{}

func (*layerCancelError) Error() string { return "layer canceled" }
