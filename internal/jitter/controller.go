package jitter

import (
	"sort"
	"time"

	"jitterlab/internal/conceal"
	"jitterlab/internal/metrics"
	"jitterlab/internal/protocol"
)

// Config tunes the playout controller. All timing is driven by caller
// supplied event times, so tests never depend on a real clock.
type Config struct {
	FrameInterval    time.Duration // nominal frame duration, e.g. 20ms
	BaseTargetMs     float64       // configured target playout delay
	MinTargetMs      float64       // adaptive target lower bound
	MaxTargetMs      float64       // adaptive target upper bound
	JitterMargin     float64       // multiplier on the jitter estimate
	WindowSize       int           // max buffered packets per stream
	SampleInterval   time.Duration // watermark sampling period
	ConcealThreshold int           // run length flagged unrecoverable
	// MaxSendRegressionMs bounds tolerated send-timestamp backtrack from
	// reordering; larger regressions are rejected as malformed.
	MaxSendRegressionMs int64
}

// Output is one emitted playout slot.
type Output struct {
	Frame conceal.Frame
	At    time.Duration
}

type stream struct {
	win         *Window
	started     bool // playout clock initialized
	playoutAt   time.Duration
	nextSample  time.Duration
	lastGood    *protocol.Packet
	concealRun  int
	jitterMs    float64 // RFC 3550 interarrival jitter estimate
	lastTransit float64
	hasTransit  bool
	maxSendMs   int64
	hasSend     bool
	targetMs    float64
}

// Controller is the shared jitter-buffer/playout engine used by both the
// UDP receiver and the offline replayer.
type Controller struct {
	cfg     Config
	streams map[uint32]*stream
	// M aggregates all counters and watermark samples.
	M *metrics.Metrics
	// OnOutput, if set, receives every emitted slot in playout order.
	OnOutput func(Output)
}

func NewController(cfg Config) *Controller {
	return &Controller{cfg: cfg, streams: make(map[uint32]*stream), M: &metrics.Metrics{}}
}

func (c *Controller) stream(id uint32) *stream {
	s, ok := c.streams[id]
	if !ok {
		s = &stream{win: NewWindow(c.cfg.WindowSize)}
		c.streams[id] = s
	}
	return s
}

// AddPacket feeds one decoded packet observed at arrival (event time).
// Semantically invalid packets (send-timestamp regression) are counted as
// malformed and never advance playout state.
func (c *Controller) AddPacket(p protocol.Packet, arrival time.Duration) {
	s := c.stream(p.StreamID)
	if s.hasSend && p.SendTimeMs < s.maxSendMs-c.cfg.MaxSendRegressionMs {
		c.M.Malformed++
		return
	}
	if !s.hasSend || p.SendTimeMs > s.maxSendMs {
		s.maxSendMs = p.SendTimeMs
		s.hasSend = true
	}
	c.M.Received++

	// RFC 3550 style jitter estimate on transit time; clock drift shows up
	// as a slow transit ramp and is absorbed by the estimator.
	transit := ms(arrival) - float64(p.SendTimeMs)
	if s.hasTransit {
		d := transit - s.lastTransit
		if d < 0 {
			d = -d
		}
		s.jitterMs += (d - s.jitterMs) / 16
	}
	s.lastTransit = transit
	s.hasTransit = true
	s.targetMs = clamp(c.cfg.BaseTargetMs+c.cfg.JitterMargin*s.jitterMs,
		c.cfg.MinTargetMs, c.cfg.MaxTargetMs)

	switch s.win.Insert(p) {
	case Duplicate:
		c.M.DuplicateDropped++
	case Late, Full:
		c.M.LateDropped++
	}
	if !s.started && s.win.Has(s.win.Base()) {
		s.started = true
		s.playoutAt = arrival + dur(s.targetMs)
		s.nextSample = s.playoutAt
	}
}

// PlayoutUntil emits every slot whose deadline falls at or before t.
func (c *Controller) PlayoutUntil(t time.Duration) {
	for _, id := range c.sortedStreams() {
		s := c.streams[id]
		for s.started && s.playoutAt <= t {
			c.step(id, s)
		}
	}
}

// Drain flushes end-of-stream: frames still in the window that can be
// output in order are played; unfillable gaps are concealed by the same
// deadline rules so tail continuity is recorded.
func (c *Controller) Drain() {
	for _, id := range c.sortedStreams() {
		s := c.streams[id]
		for s.started && s.win.Len() > 0 {
			c.step(id, s)
		}
	}
}

// step emits exactly one slot for stream id at its current deadline.
func (c *Controller) step(id uint32, s *stream) {
	var f conceal.Frame
	if p, ok := s.win.Pop(); ok {
		f = conceal.FromReal(p)
		cp := p
		s.lastGood = &cp
		s.concealRun = 0
		c.M.RecordPlayed()
	} else {
		seq := s.win.Base()
		s.win.Skip()
		s.concealRun++
		f = conceal.Substitute(s.lastGood, id, seq, s.concealRun, c.cfg.ConcealThreshold)
		c.M.RecordConceal(f.Unrecoverable)
	}
	if c.OnOutput != nil {
		c.OnOutput(Output{Frame: f, At: s.playoutAt})
	}
	s.playoutAt += c.cfg.FrameInterval
	for s.nextSample <= s.playoutAt {
		c.M.Sample(metrics.Watermark{
			TimeMs:         ms(s.nextSample),
			StreamID:       id,
			BufferedFrames: s.win.Len(),
			TargetDelayMs:  s.targetMs,
		})
		s.nextSample += c.cfg.SampleInterval
	}
}

func (c *Controller) sortedStreams() []uint32 {
	ids := make([]uint32, 0, len(c.streams))
	for id := range c.streams {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func dur(ms float64) time.Duration { return time.Duration(ms * float64(time.Millisecond)) }

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
