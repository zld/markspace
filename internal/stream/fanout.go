package stream

import (
	"fmt"
	"sync"

	"mediad/internal/clock"
)

// LayerConfig configures one representation layer's queue and target bitrate.
type LayerConfig struct {
	// Name is the representation label (e.g. "450k").
	Name string
	// TargetBPS is the target bitrate in bits per second.
	TargetBPS int
	// QueueCap is the bounded queue capacity in packets.
	QueueCap int
	// Policy is PolicyBlock or PolicyDrop.
	Policy string
}

// Fanout distributes an input stream to independent per-layer bounded
// queues. One slow layer cannot grow memory or stall other layers beyond the
// configured policy; every queue retains a fixed packet bound.
type Fanout struct {
	mu       sync.Mutex
	layers   []*layer
	closed   bool
	wg       sync.WaitGroup
	produced int64
}

type layer struct {
	cfg     LayerConfig
	index   int
	queue   *Queue
	tracker *clock.SeqTracker
	unwrap  *clock.Unwrapper
	// damaged marks a layer isolated after an unrecoverable fault.
	damaged bool
	// recovered counts successful boundary recoveries.
	recovered int
}

// NewFanout builds a fan-out with at least the supplied layers.
func NewFanout(cfgs []LayerConfig) (*Fanout, error) {
	if len(cfgs) < 1 {
		return nil, fmt.Errorf("stream: fan-out needs at least one layer")
	}
	f := &Fanout{}
	for i, cfg := range cfgs {
		q, err := NewQueue(cfg.QueueCap, cfg.Policy)
		if err != nil {
			return nil, clock.WrapError(clock.StageStream, uint32(i+1), false, err)
		}
		f.layers = append(f.layers, &layer{
			cfg: cfg, index: i, queue: q,
			tracker: &clock.SeqTracker{}, unwrap: clock.NewRTPUnwrapper(),
		})
	}
	return f, nil
}

// Layers returns the number of representation layers.
func (f *Fanout) Layers() int { return len(f.layers) }

// Queue exposes a layer's bounded queue, mainly for final draining.
func (f *Fanout) Queue(layer int) *Queue {
	if layer < 0 || layer >= len(f.layers) {
		return nil
	}
	return f.layers[layer].queue
}

// MarkDamaged isolates one layer; its Publish calls then fail with a
// recoverable error while other layers keep running.
func (f *Fanout) MarkDamaged(layer int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if layer >= 0 && layer < len(f.layers) {
		f.layers[layer].damaged = true
	}
}

// Recover clears a layer's damage flag and records a boundary recovery.
func (f *Fanout) Recover(layer int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if layer >= 0 && layer < len(f.layers) && f.layers[layer].damaged {
		f.layers[layer].damaged = false
		f.layers[layer].recovered++
	}
}

// Publish hands one source envelope to a single target layer, applying that
// layer's sequence tracking and overflow policy. A damaged layer rejects
// input with a recoverable error until Recover is called.
func (f *Fanout) Publish(e Envelope) error {
	if e.Layer < 0 || e.Layer >= len(f.layers) {
		return clock.TrackError(clock.StageStream, uint32(e.Layer+1),
			"layer index out of range", false)
	}
	f.mu.Lock()
	l := f.layers[e.Layer]
	if l.damaged {
		f.mu.Unlock()
		return clock.TrackError(clock.StageStream, uint32(e.Layer+1),
			"layer damaged, awaiting recovery", true)
	}
	f.produced++
	f.mu.Unlock()

	// Sequence classification (loss/dup/reorder) is recorded on the layer's
	// tracker; Publish itself succeeds so ordering faults stay recoverable.
	l.tracker.Next(e.Seq)
	return l.queue.Enqueue(e)
}

// Drain runs a consumer for one layer, invoking handle for each dequeued
// envelope until the layer queue is closed and emptied.
func (f *Fanout) Drain(layerIdx int, handle func(Envelope)) error {
	if layerIdx < 0 || layerIdx >= len(f.layers) {
		return clock.TrackError(clock.StageStream, uint32(layerIdx+1),
			"layer index out of range", false)
	}
	q := f.layers[layerIdx].queue
	f.wg.Add(1)
	defer f.wg.Done()
	for e, ok := q.Dequeue(); ok; e, ok = q.Dequeue() {
		handle(e)
	}
	return nil
}

// Close closes every layer queue; in-flight Drain calls finish after drain.
func (f *Fanout) Close() {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	for _, l := range f.layers {
		l.queue.Close()
	}
	f.wg.Wait()
}

// Stats aggregates every layer's queue snapshot.
func (f *Fanout) Stats() []LayerStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]LayerStats, 0, len(f.layers))
	for _, l := range f.layers {
		s := l.queue.Snapshot(l.index)
		out = append(out, LayerStats{
			Index: l.index, Name: l.cfg.Name, TargetBPS: l.cfg.TargetBPS,
			Len: s.Len, Cap: s.Cap, Dropped: s.Dropped, Policy: s.Policy,
			Damaged: l.damaged, Recovered: l.recovered,
			Lost: l.tracker.Lost, Duplicates: l.tracker.Duplicates, Reordered: l.tracker.Reordered,
		})
	}
	return out
}

// LayerStats is one layer's runtime counters.
type LayerStats struct {
	Index      int
	Name       string
	TargetBPS  int
	Len        int
	Cap        int
	Dropped    int64
	Policy     string
	Damaged    bool
	Recovered  int
	Lost       int64
	Duplicates int64
	Reordered  int64
}

// TotalDropped is the sum of dropped packets across all layers.
func (f *Fanout) TotalDropped() int64 {
	var n int64
	for _, s := range f.Stats() {
		n += s.Dropped
	}
	return n
}
