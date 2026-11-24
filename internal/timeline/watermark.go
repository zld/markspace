// Package timeline advances a monotonic event-time watermark over corrected
// media timestamps, orders variable-framerate samples by their own timestamps,
// and records which events become late as the watermark moves.
package timeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"mediasync/internal/model"
)

// Sample is one media sample as the timeline sees it. OriginalTime is the
// timestamp as captured and CorrectedTime is the drift-corrected value on the
// shared time base. Both are retained so output can report the correction.
type Sample struct {
	Index         int64
	TrackID       uint32
	Kind          model.TrackKind
	Keyframe      bool
	ExtSeq        int64
	OriginalTime  model.Nanos
	CorrectedTime model.Nanos
	Arrival       model.Nanos
	Size          int
}

// Config controls watermark progression.
type Config struct {
	// AllowedLateness is how far behind the newest event time an event may
	// arrive before it is considered late. It must be >= 0.
	AllowedLateness model.Nanos
	// MaxSamples caps retained samples so memory stays bounded.
	MaxSamples int
}

// Watermark is a monotonic event-time cursor.
type Watermark struct {
	cfg      Config
	value    model.Nanos
	maxEvent model.Nanos
	started  bool
}

// NewWatermark validates config and returns an unstarted watermark.
func NewWatermark(cfg Config) (*Watermark, error) {
	if cfg.AllowedLateness < 0 {
		return nil, model.NewError(model.CodeTimelineLate, model.Loc{TrackIndex: -1}, "allowed lateness must not be negative, got %v", cfg.AllowedLateness)
	}
	if cfg.MaxSamples <= 0 {
		return nil, model.NewError(model.CodeBufferOverflow, model.Loc{TrackIndex: -1}, "MaxSamples must be positive, got %d", cfg.MaxSamples)
	}
	return &Watermark{cfg: cfg}, nil
}

// Advance moves the watermark forward based on an observed event time. The
// watermark never moves backwards: out-of-order event times leave it
// unchanged, which is what makes replans deterministic. It returns true when
// the watermark moved.
func (w *Watermark) Advance(et model.Nanos) bool {
	if !w.started || et > w.maxEvent {
		w.maxEvent = et
		w.started = true
	}
	next := w.maxEvent - w.cfg.AllowedLateness
	if next <= w.value {
		return false
	}
	w.value = next
	return true
}

// Value returns the current watermark.
func (w *Watermark) Value() model.Nanos { return w.value }

// MaxEventTime returns the largest event time observed.
func (w *Watermark) MaxEventTime() model.Nanos { return w.maxEvent }

// Started reports whether any event has been observed.
func (w *Watermark) Started() bool { return w.started }

// IsLate reports whether an event with the given corrected time would fall
// behind the watermark.
func (w *Watermark) IsLate(ct model.Nanos) bool { return w.started && ct < w.value }

// Restore sets the watermark state from a checkpoint.
func (w *Watermark) Restore(value, maxEvent model.Nanos, started bool) {
	w.value, w.maxEvent, w.started = value, maxEvent, started
}

// Timeline accumulates samples in corrected media order. Ordering uses the
// sample timestamps directly, so variable frame rates are preserved exactly:
// the kernel never synthesizes samples on a fixed grid.
type Timeline struct {
	cfg     Config
	wm      *Watermark
	samples []Sample
	late    int64
	trunc   int64
}

// New creates a timeline.
func New(cfg Config) (*Timeline, error) {
	wm, err := NewWatermark(cfg)
	if err != nil {
		return nil, err
	}
	return &Timeline{cfg: cfg, wm: wm}, nil
}

// OfferResult reports what happened when a sample was offered.
type OfferResult struct {
	Added bool
	Late  bool
	// Evicted is the number of oldest samples dropped because MaxSamples was
	// reached. Evictions are never silent: the caller must account for them.
	Evicted int
}

// Offer places a sample. Samples that are older than the watermark are
// rejected with Late set and must be routed through replay. The sample slice
// stays ordered by corrected event time, then track id, then extended
// sequence so equal timestamps have a stable order.
func (t *Timeline) Offer(s Sample) OfferResult {
	res := OfferResult{}
	if t.wm.IsLate(s.CorrectedTime) {
		t.late++
		res.Late = true
		return res
	}
	t.wm.Advance(s.CorrectedTime)
	pos := sort.Search(len(t.samples), func(i int) bool {
		return less(s, t.samples[i])
	})
	t.samples = append(t.samples, Sample{})
	copy(t.samples[pos+1:], t.samples[pos:])
	t.samples[pos] = s
	res.Added = true
	for len(t.samples) > t.cfg.MaxSamples {
		t.samples = t.samples[1:]
		t.trunc++
		res.Evicted++
	}
	return res
}

func less(a, b Sample) bool {
	if a.CorrectedTime != b.CorrectedTime {
		return a.CorrectedTime < b.CorrectedTime
	}
	if a.TrackID != b.TrackID {
		return a.TrackID < b.TrackID
	}
	return a.Index < b.Index
}

// Samples returns a copy of the ordered sample window.
func (t *Timeline) Samples() []Sample {
	return append([]Sample(nil), t.samples...)
}

// Watermark returns the current watermark.
func (t *Timeline) Watermark() model.Nanos { return t.wm.Value() }

// MarkLate accounts for a sample rejected as late without buffering it.
func (t *Timeline) MarkLate() { t.late++ }

// Stats returns counters for summaries.
func (t *Timeline) Stats() (late, truncated int64, kept int) {
	return t.late, t.trunc, len(t.samples)
}

// Reset discards samples and watermark, used before a deterministic replay.
func (t *Timeline) Reset() {
	t.samples = nil
	t.late = 0
	t.trunc = 0
	t.wm = &Watermark{cfg: t.cfg}
}

// Snapshot is the serializable timeline state stored in a checkpoint.
type Snapshot struct {
	Watermark  model.Nanos
	MaxEvent   model.Nanos
	Started    bool
	Samples    []Sample
	LateCount  int64
	TruncCount int64
}

// Snapshot captures state for checkpointing.
func (t *Timeline) Snapshot() Snapshot {
	return Snapshot{
		Watermark:  t.wm.Value(),
		MaxEvent:   t.wm.MaxEventTime(),
		Started:    t.wm.Started(),
		Samples:    append([]Sample(nil), t.samples...),
		LateCount:  t.late,
		TruncCount: t.trunc,
	}
}

// Restore replaces timeline state from a snapshot.
func (t *Timeline) Restore(s Snapshot) {
	t.wm.Restore(s.Watermark, s.MaxEvent, s.Started)
	t.samples = append([]Sample(nil), s.Samples...)
	t.late = s.LateCount
	t.trunc = s.TruncCount
}

// RestoreCursor restores only the watermark and counters, dropping the
// bounded sample display window. The window never influences output, so a
// resumed run and an uninterrupted run still agree.
func (t *Timeline) RestoreCursor(watermark, maxEvent model.Nanos, started bool, late, trunc int64) {
	t.wm.Restore(watermark, maxEvent, started)
	t.samples = nil
	t.late = late
	t.trunc = trunc
}

// Insert adds a sample without consulting the watermark. It is used by
// deterministic replay: a sample that was too late for the live watermark is
// still inserted at its correct timestamp, never dropped or clamped.
func (t *Timeline) Insert(s Sample) {
	pos := sort.Search(len(t.samples), func(i int) bool {
		return less(s, t.samples[i])
	})
	t.samples = append(t.samples, Sample{})
	copy(t.samples[pos+1:], t.samples[pos:])
	t.samples[pos] = s
	for len(t.samples) > t.cfg.MaxSamples {
		t.samples = t.samples[1:]
		t.trunc++
	}
}

// TimestampOrderDigest returns a stable digest of the current sample order,
// including both original and corrected times. It demonstrates that samples
// are ordered by their own timestamps and is used by tests.
func (t *Timeline) TimestampOrderDigest() string {
	h := sha256.New()
	for _, s := range t.samples {
		fmt.Fprintf(h, "%d|%d|%d|%d|%d\n", s.TrackID, s.Index, s.ExtSeq, s.OriginalTime, s.CorrectedTime)
	}
	return hex.EncodeToString(h.Sum(nil))
}
