package rtp

import (
	"sync"
)

// Window is a bounded, order-restoring jitter buffer. Push is safe for
// concurrent use.
type Window struct {
	cfg Config

	mu          sync.Mutex
	started     bool
	highest     int64
	floor       int64 // first sequence not yet released
	baseTS      uint32
	prevTS      uint32
	prevSeq     int64
	havePrev    bool
	pending     map[int64]*Frame
	seen        map[int64]struct{} // recently delivered, for duplicate detection
	seenOrder   []int64            // FIFO ordering of seen keys, bounded
	gapPending  map[int64]Gap      // known-missing positions not yet emitted
	stats       Stats
	maxResident int
}

// NewWindow builds a window from cfg, validating it eagerly.
func NewWindow(cfg Config) (*Window, error) {
	validated, err := cfg.validate()
	if err != nil {
		return nil, err
	}
	return &Window{
		cfg:        validated,
		pending:    make(map[int64]*Frame, validated.Depth*2+1),
		seen:       make(map[int64]struct{}, validated.Depth*4+4),
		gapPending: make(map[int64]Gap),
	}, nil
}

// Cap reports the configured window depth.
func (w *Window) Cap() int { return w.cfg.Depth }

// Config returns the validated configuration.
func (w *Window) Config() Config { return w.cfg }

// Stats returns a snapshot of the counters.
func (w *Window) Stats() Stats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stats
}

// MaxResident reports the largest number of frames the window ever held.
func (w *Window) MaxResident() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.maxResident
}

// unwrap maps a 16-bit sequence number onto the extended sequence line,
// interpreting the distance to the highest seen number as signed.
func (w *Window) unwrap(seq uint16) int64 {
	if !w.started {
		return int64(seq)
	}
	delta := int64(int16(seq - uint16(w.highest)))
	return w.highest + delta
}
