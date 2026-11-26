// Package pipeline moves items from a producer to a consumer through a
// byte-bounded queue. When the queue is full the producer blocks, applying
// backpressure upstream; the peak buffered bytes are reported.
package pipeline

import "sync"

// Item is one unit of work flowing through the pipeline.
type Item struct {
	SizeBytes int64
	Value     any
}

// Stats reports a pipeline run.
type Stats struct {
	PeakBytes int64
	Processed int
}

// queue is a byte-bounded blocking FIFO.
type queue struct {
	mu       sync.Mutex
	notFull  *sync.Cond
	notEmpty *sync.Cond
	items    []Item
	bytes    int64
	cap      int64
	peak     int64
	closed   bool
}

func newQueue(capBytes int64) *queue {
	if capBytes <= 0 {
		capBytes = 1 << 20
	}
	q := &queue{cap: capBytes}
	q.notFull = sync.NewCond(&q.mu)
	q.notEmpty = sync.NewCond(&q.mu)
	return q
}

// push blocks while the queue is full (backpressure). An item larger than
// the capacity passes only when the queue is completely empty.
func (q *queue) push(it Item) {
	q.mu.Lock()
	for q.bytes > 0 && q.bytes+it.SizeBytes > q.cap {
		q.notFull.Wait()
	}
	q.items = append(q.items, it)
	q.bytes += it.SizeBytes
	if q.bytes > q.peak {
		q.peak = q.bytes
	}
	q.notEmpty.Signal()
	q.mu.Unlock()
}

func (q *queue) pop() (Item, bool) {
	q.mu.Lock()
	for len(q.items) == 0 && !q.closed {
		q.notEmpty.Wait()
	}
	if len(q.items) == 0 {
		q.mu.Unlock()
		return Item{}, false
	}
	it := q.items[0]
	q.items = q.items[1:]
	q.bytes -= it.SizeBytes
	q.notFull.Signal()
	q.mu.Unlock()
	return it, true
}

func (q *queue) close() {
	q.mu.Lock()
	q.closed = true
	q.notEmpty.Broadcast()
	q.mu.Unlock()
}

// Config configures a run.
type Config struct {
	Memory int64 // maximum buffered bytes
}

// Run streams items from produce to consume through the bounded queue.
// Items are consumed in production order, so results are deterministic.
func Run(cfg Config, produce func(push func(Item)), consume func(Item)) Stats {
	q := newQueue(cfg.Memory)
	go func() {
		produce(q.push)
		q.close()
	}()
	var st Stats
	for {
		it, ok := q.pop()
		if !ok {
			break
		}
		consume(it)
		st.Processed++
	}
	st.PeakBytes = q.peak
	return st
}
