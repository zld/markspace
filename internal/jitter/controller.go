package jitter

import (
	"time"

	"jitterlab/internal/metrics"
	"jitterlab/internal/plc"
)

// Controller turns an arrival stream into a paced playout of real and
// concealed frames. Both receive paths feed the same instance.
type Controller struct {
	cfg        Config
	q          *OrderedQueue
	m          *metrics.Summary
	streams    map[uint16]*playState
	ids        []uint16
	now        time.Duration
	sampleEach time.Duration
	nextSample time.Duration

	received      int64
	invalid       int64
	interruptions int64
	inputClosed   bool
	sampling      bool
}

type playState struct {
	id         uint16
	started    bool
	playAt     time.Duration
	waiting    bool
	waitUntil  time.Duration
	catchingUp bool

	lastArrival time.Duration
	haveArrival bool
	// firstArrival and baseTS anchor the sender's capture clock onto the
	// receiver's clock, so capture timestamps can be compared with arrival
	// times even though the two clocks are not synchronised.
	firstArrival time.Duration
	baseTS       uint32

	haveTransit bool
	lastTransit time.Duration
	jitterMS    float64
	targetMS    float64

	ended     bool
	suspended bool
	conceal   *plc.Concealer
}

// NewController builds a controller and a metrics summary if none is supplied.
func NewController(cfg Config, m *metrics.Summary) *Controller {
	if m == nil {
		m = metrics.New()
	}
	cfg = cfg.normalized()
	return &Controller{
		cfg:        cfg,
		q:          NewQueue(cfg.Queue),
		m:          m,
		streams:    make(map[uint16]*playState),
		sampleEach: time.Duration(cfg.SampleMS) * time.Millisecond,
		nextSample: 0,
	}
}

// Summary exposes the metrics object for the caller finalising a report.
func (c *Controller) Summary() *metrics.Summary { return c.m }

// QueueStats returns the current queue disposition counters.
func (c *Controller) QueueStats() QueueStats { return c.q.Stats() }

// StreamCount reports the number of tracked streams.
func (c *Controller) StreamCount() int { return c.q.StreamCount() }

// StreamIDs returns tracked stream ids in ascending order.
func (c *Controller) StreamIDs() []uint16 { return c.q.StreamIDs() }

// Buffered reports the packet count for one stream.
func (c *Controller) Buffered(streamID uint16) int { return c.q.Buffered(streamID) }

// HasWork reports whether any tracked stream is still playing out frames. It
// lets a caller wait for the buffered tail before finalising.
func (c *Controller) HasWork() bool {
	for _, id := range c.ids {
		if st := c.streams[id]; st != nil && st.started && !st.ended {
			return true
		}
	}
	return false
}

// TargetMS reports the current adaptive target for a stream.
func (c *Controller) TargetMS(streamID uint16) (float64, bool) {
	st, ok := c.streams[streamID]
	if !ok {
		return 0, false
	}
	return st.targetMS, true
}

// Now returns the controller's playout clock.
func (c *Controller) Now() time.Duration { return c.now }

// CloseInput marks that no further packets will be fed. Frames buffered at that
// moment are still played; once a stream's queue is empty the playout clock
// stops instead of concealing forever, so end-of-stream tails are not reported
// as hidden frames.
func (c *Controller) CloseInput() {
	c.inputClosed = true
}
