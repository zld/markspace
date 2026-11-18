// Package buffer implements the adaptive playout buffer shared by the live
// UDP receiver and the offline replay driver.
//
// The controller owns the reorder window, the playout clock and the
// concealment policy. Callers feed decoded frames with a timestamp and
// advance a monotonic millisecond clock; both receiver paths use exactly
// the same state machine, which is what makes replay statistics
// reproducible.
package buffer

import (
	"fmt"
	"sync"

	"jitterlab/internal/conceal"
	"jitterlab/internal/packet"
)

const (
	// MaxFrameMs bounds the configured frame duration.
	MaxFrameMs = 500
	// MaxDelayMs bounds target and maximum wait latency.
	MaxDelayMs = 10_000
	// maxCatchUp limits how many slots one tick may emit after a stall.
	maxCatchUp = 8
	// jitterAlpha weights each new arrival-jitter sample.
	jitterAlpha = 0.25
)

// Config carries the user-selectable buffer parameters.
type Config struct {
	FrameMs    int
	TargetMs   int
	MaxWaitMs  int
	WindowSize int
}

// Validate rejects parameters that cannot produce a coherent buffer.
func (c Config) Validate() error {
	if c.FrameMs <= 0 || c.FrameMs > MaxFrameMs {
		return fmt.Errorf("buffer: frame-ms %d must be in 1..%d", c.FrameMs, MaxFrameMs)
	}
	if c.TargetMs <= 0 || c.TargetMs > MaxDelayMs {
		return fmt.Errorf("buffer: target-ms %d must be in 1..%d", c.TargetMs, MaxDelayMs)
	}
	if c.MaxWaitMs <= 0 || c.MaxWaitMs > MaxDelayMs {
		return fmt.Errorf("buffer: max-wait-ms %d must be in 1..%d", c.MaxWaitMs, MaxDelayMs)
	}
	if c.TargetMs > c.MaxWaitMs {
		return fmt.Errorf("buffer: target-ms %d exceeds max-wait-ms %d", c.TargetMs, c.MaxWaitMs)
	}
	if c.WindowSize <= 0 || c.WindowSize > MaxWindowSize {
		return fmt.Errorf("buffer: window %d must be in 1..%d", c.WindowSize, MaxWindowSize)
	}
	return nil
}

// Stats is a snapshot of controller counters. Dropped counts packets that
// never reached playout (expired, shed for latency, or abandoned under a
// concealed slot); duplicates and reordered packets are tracked separately.
type Stats struct {
	Offered       int
	Duplicates    int
	Reordered     int
	Dropped       int
	Concealed     int
	Silent        int
	Recoveries    int
	MaxConcealRun int
	ContinuitySum float64
	Started       bool
	Finished      bool
	PlayoutSlots  int
}

// Slot describes one playout clock slot.
type Slot struct {
	Played     bool
	TimeMs     int64
	Seq        uint64 // wire sequence of the slot, concealed or real
	Kind       conceal.Kind
	Payload    [packet.PayloadSize]byte
	BufferMs   int
	TargetMs   int
	ConcealRun int
	// Recovered is set on the first real frame after a conceal run and
	// Delta is the sample jump against the last concealed payload.
	Recovered bool
	Delta     float64
	Finished  bool
}

// Controller is safe for concurrent use by one producer and one playout
// goroutine.
type Controller struct {
	cfg Config
	mu  sync.Mutex

	win *Window
	plc *conceal.Concealer

	baseFrames   int
	targetFrames int
	minFrames    int
	maxFrames    int
	jitterMs     float64
	haveArrival  bool
	lastArrival  int64
	firstArrival int64

	started    bool
	inputDone  bool
	finished   bool
	nextPlayAt int64
	stats      Stats
}

// New validates cfg and builds a controller. decay is the concealment
// attenuation applied per concealed slot.
func New(cfg Config, decay float64) (*Controller, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	win, err := NewWindow(cfg.WindowSize)
	if err != nil {
		return nil, err
	}
	plc, err := conceal.New(maxConcealRun(cfg), decay)
	if err != nil {
		return nil, err
	}
	base := ceilDiv(cfg.TargetMs, cfg.FrameMs)
	maxFrames := ceilDiv(cfg.MaxWaitMs, cfg.FrameMs)
	minFrames := base / 2
	if minFrames < 1 {
		minFrames = 1
	}
	if maxFrames < base {
		maxFrames = base
	}
	return &Controller{
		cfg:          cfg,
		win:          win,
		plc:          plc,
		baseFrames:   base,
		targetFrames: clamp(base, minFrames, maxFrames),
		minFrames:    minFrames,
		maxFrames:    maxFrames,
	}, nil
}

// maxConcealRun allows roughly half a second of concealment.
func maxConcealRun(cfg Config) int {
	run := 500 / cfg.FrameMs
	if run < 1 {
		run = 1
	}
	return run
}

// Offer feeds one successfully decoded frame arriving at arrivalMs.
func (c *Controller) Offer(f packet.Frame, arrivalMs int64) PushResult {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stats.Offered++
	if !c.haveArrival {
		c.haveArrival = true
		c.firstArrival = arrivalMs
	} else {
		c.sampleJitter(arrivalMs)
	}
	c.lastArrival = arrivalMs
	c.adaptTarget()

	res := c.win.Push(f)
	switch res.Status {
	case Accepted:
		if res.Reordered {
			c.stats.Reordered++
		}
	case Duplicate:
		c.stats.Duplicates++
	case Expired:
		c.stats.Dropped++
	}
	return res
}

// sampleJitter smooths the deviation of the inter-arrival interval from
// the nominal frame duration.
func (c *Controller) sampleJitter(arrivalMs int64) {
	delta := arrivalMs - c.lastArrival
	dev := delta - int64(c.cfg.FrameMs)
	if dev < 0 {
		dev = -dev
	}
	// Clamp outliers so one very late packet does not dominate.
	if cap := int64(4 * c.cfg.FrameMs); dev > cap {
		dev = cap
	}
	c.jitterMs += jitterAlpha * (float64(dev) - c.jitterMs)
}

// adaptTarget moves the target water level towards the smoothed jitter,
// clamped to the configured bounds and rate limited to one frame per
// update.
func (c *Controller) adaptTarget() {
	desired := c.baseFrames + ceilDiv(int(c.jitterMs*2), c.cfg.FrameMs)
	desired = clamp(desired, c.minFrames, c.maxFrames)
	switch {
	case desired > c.targetFrames:
		c.targetFrames++
	case desired < c.targetFrames:
		c.targetFrames--
	}
}

// Tick advances the playout clock to nowMs and returns at most one
// playable slot. Callers drive it repeatedly; slots missed during a stall
// are caught up up to maxCatchUp per call. Finished is set once Finish was
// called and all buffered audio has been played.
func (c *Controller) Tick(nowMs int64) Slot {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.finished {
		return Slot{Finished: true, TimeMs: nowMs}
	}
	if c.inputDone && c.drained() {
		c.finished = true
		c.stats.Finished = true
		return Slot{Finished: true, TimeMs: nowMs}
	}
	if !c.started {
		if !c.readyToStart(nowMs) {
			return Slot{BufferMs: c.bufferMs(), TargetMs: c.targetMs()}
		}
		c.started = true
		c.nextPlayAt = nowMs
		c.stats.Started = true
	}
	if nowMs < c.nextPlayAt {
		return Slot{BufferMs: c.bufferMs(), TargetMs: c.targetMs()}
	}

	slot := c.play(nowMs)
	slot.BufferMs = c.bufferMs()
	slot.TargetMs = c.targetMs()
	c.stats.PlayoutSlots++
	if c.finished && c.drained() {
		c.stats.Finished = true
		slot.Finished = true
	}
	return slot
}

// readyToStart waits for the target water level, or for the maximum wait
// to elapse, or for the stream to end with whatever audio it has.
func (c *Controller) readyToStart(nowMs int64) bool {
	if !c.win.Initialized() {
		return false
	}
	if c.win.Len() >= c.targetFrames {
		return true
	}
	if c.inputDone {
		return true
	}
	return nowMs-c.firstArrival >= int64(c.cfg.MaxWaitMs)
}

// play emits one slot: a real frame when available, otherwise concealment.
func (c *Controller) play(nowMs int64) Slot {
	c.nextPlayAt += int64(c.cfg.FrameMs)
	c.shedLatency()

	seq := c.win.Next()
	if f, ok := c.win.Pop(); ok {
		out, delta, recovered := c.plc.Present(f)
		if recovered {
			c.stats.Recoveries++
			c.stats.ContinuitySum += delta
		}
		return Slot{
			Played:     true,
			TimeMs:     nowMs,
			Seq:        packet.MaskSeq(seq),
			Kind:       out.Kind,
			Payload:    out.Payload,
			ConcealRun: 0,
			Recovered:  recovered,
			Delta:      delta,
		}
	}

	// Nothing at the playout point: conceal this slot. SkipNext keeps the
	// clock moving so a late packet can never overwrite it; a frame that
	// was still buffered but out of order is counted as dropped.
	out := c.plc.Conceal()
	if c.win.HasNext() {
		c.stats.Dropped++
	}
	c.win.SkipNext()
	c.stats.Concealed++
	if out.Kind == conceal.KindSilence {
		c.stats.Silent++
	}
	if out.Run > c.stats.MaxConcealRun {
		c.stats.MaxConcealRun = out.Run
	}
	return Slot{
		Played:     true,
		TimeMs:     nowMs,
		Seq:        packet.MaskSeq(seq),
		Kind:       out.Kind,
		Payload:    out.Payload,
		ConcealRun: out.Run,
	}
}

// shedLatency drops the oldest buffered frames when the water level has
// grown beyond the configured maximum, trading continuity for latency.
func (c *Controller) shedLatency() {
	highest := c.win.Next()
	if h, ok := c.win.Highest(); ok {
		if h > highest {
			highest = h
		}
	}
	span := int(highest-c.win.Next()) + 1
	if !c.win.Initialized() || span <= c.maxFrames {
		return
	}
	target := highest - uint64(c.maxFrames) + 1
	if n := c.win.SkipTo(target); n > 0 {
		c.stats.Dropped += n
	}
}

// Finish marks the input stream complete. The controller keeps playing
// buffered audio, then reports a Finished slot.
func (c *Controller) Finish() {
	c.mu.Lock()
	c.inputDone = true
	c.mu.Unlock()
}

// drained reports that no buffered frame remains at or beyond the playout
// point, so concealment would extend past the end of the stream.
func (c *Controller) drained() bool {
	h, ok := c.win.Highest()
	return !ok || c.win.Len() == 0 || c.win.Next() > h
}

// Snapshot returns a copy of the counters.
func (c *Controller) Snapshot() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

// FrameMs returns the configured frame duration.
func (c *Controller) FrameMs() int { return c.cfg.FrameMs }

func (c *Controller) targetMs() int { return c.targetFrames * c.cfg.FrameMs }

func (c *Controller) bufferMs() int {
	return c.win.Len() * c.cfg.FrameMs
}

func ceilDiv(a, b int) int {
	if a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
