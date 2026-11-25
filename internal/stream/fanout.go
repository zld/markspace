// Package stream implements multi-bitrate fan-out with bounded per-layer
// queues and a shared-clock fan-in. A full queue applies back-pressure to the
// upstream producer; a consumer that stays blocked past its deadline is
// isolated so the other representations keep progressing, and the dropped
// time interval is recorded rather than silently discarded.
package stream

import (
	"sync"
	"time"

	"packcheck/internal/model"
)

// Item is one work unit on the shared clock.
type Item struct {
	Sequence   int
	Timestamp  uint32 // RTP clock units
	ByteOffset int64
	Payload    []byte
}

// DroppedInterval records the time span lost when a slow layer was isolated.
type DroppedInterval struct {
	Representation string
	StartTS        uint32
	EndTS          uint32
	StartOffset    int64
	EndOffset      int64
}

// Layer is one bounded consumer queue.
type Layer struct {
	name     string
	ch       chan Item
	peak     int
	isolated bool
	closed   bool
	mu       sync.Mutex
}

// Enqueue places an item, blocking (back-pressure) once the queue is full.
func (l *Layer) Enqueue(it Item) {
	l.ch <- it
	l.mu.Lock()
	if n := len(l.ch); n > l.peak {
		l.peak = n
	}
	l.mu.Unlock()
}

// Drain returns the receive channel for consumer goroutines.
func (l *Layer) Drain() <-chan Item { return l.ch }

// Isolate marks the layer as removed from the active fan-out set.
func (l *Layer) Isolate() {
	l.mu.Lock()
	l.isolated = true
	l.mu.Unlock()
}

// Isolated reports the isolation state.
func (l *Layer) Isolated() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.isolated
}

// Peak returns the high-water queue depth observed.
func (l *Layer) Peak() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.peak
}

// Name returns the representation label.
func (l *Layer) Name() string { return l.name }

// Close releases the layer channel; it is safe to call more than once.
func (l *Layer) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	close(l.ch)
}

// FanOut owns the per-layer bounded queues and the slow-consumer policy.
type FanOut struct {
	capacity int
	timeout  time.Duration
	layers   map[string]*Layer
	order    []string
	dropped  []DroppedInterval
	issues   model.IssueLog
	mu       sync.Mutex
}

// NewFanOut builds a fan-out whose every layer holds at most capacity items.
func NewFanOut(capacity int, timeout time.Duration, names ...string) *FanOut {
	f := &FanOut{capacity: capacity, timeout: timeout, layers: map[string]*Layer{}}
	for _, n := range names {
		f.layers[n] = &Layer{name: n, ch: make(chan Item, capacity)}
		f.order = append(f.order, n)
	}
	return f
}

// Layers returns the layers in declaration order.
func (f *FanOut) Layers() []*Layer {
	out := make([]*Layer, 0, len(f.order))
	for _, n := range f.order {
		out = append(out, f.layers[n])
	}
	return out
}

// Layer looks up one representation.
func (f *FanOut) Layer(name string) *Layer { return f.layers[name] }

// Send delivers one item to every non-isolated layer. A layer that cannot
// accept within timeout is isolated: its queued span is recorded as a dropped
// interval and subsequent items skip it, keeping the other layers moving.
func (f *FanOut) Send(it Item) {
	for _, name := range f.order {
		l := f.layers[name]
		if l.Isolated() {
			continue
		}
		timer := time.NewTimer(f.timeout)
		select {
		case l.ch <- it:
			l.mu.Lock()
			if n := len(l.ch); n > l.peak {
				l.peak = n
			}
			l.mu.Unlock()
			timer.Stop()
		case <-timer.C:
			f.isolate(l, it)
		}
	}
}

func (f *FanOut) isolate(l *Layer, it Item) {
	l.Isolate()
	f.mu.Lock()
	defer f.mu.Unlock()
	// The dropped span starts at the oldest unconsumed queued item. The
	// queue is drained because an isolated layer is never read again.
	startTS, startOff := it.Timestamp, it.ByteOffset
	if n := len(l.ch); n > 0 {
		oldest := <-l.ch
		startTS, startOff = oldest.Timestamp, oldest.ByteOffset
	outer:
		for {
			select {
			case <-l.ch:
			default:
				break outer
			}
		}
	}
	f.dropped = append(f.dropped, DroppedInterval{
		Representation: l.name, StartTS: startTS, EndTS: it.Timestamp,
		StartOffset: startOff, EndOffset: it.ByteOffset,
	})
	f.issues.Add(model.Issue{
		Code: model.CodeSlowConsumer, Severity: model.SeverityError,
		Layer: model.LayerStream, Representation: l.name, ByteOffset: it.ByteOffset,
		Message: "slow consumer timed out; layer isolated",
	})
}

// Dropped returns the recorded dropped intervals.
func (f *FanOut) Dropped() []DroppedInterval {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]DroppedInterval, len(f.dropped))
	copy(out, f.dropped)
	return out
}

// Issues returns the stream-layer issue log.
func (f *FanOut) Issues() *model.IssueLog { return &f.issues }

// CloseAll closes every layer channel.
func (f *FanOut) CloseAll() {
	for _, l := range f.layers {
		l.Close()
	}
}
