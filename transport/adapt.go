package transport

import (
	"math"
	"sort"
	"time"

	"github.com/zld/media-check/media"
)

// BandwidthSample is one completed receive-window measurement.
type BandwidthSample struct {
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Bytes   int       `json:"bytes"`
	Packets int       `json:"packets"`
	Bitrate float64   `json:"bitrate_bps"`
	Window  int       `json:"window"`
	Clamped bool      `json:"clamped"`
}

// AdaptOptions bounds the adaptation controller.
type AdaptOptions struct {
	Window      int           // sliding window length in measurements
	MinParallel int           // floor for the concurrency limit
	MaxParallel int           // hard ceiling for concurrency
	MinQueue    int           // floor for the queue depth
	MaxQueue    int           // hard ceiling for the queue depth
	TargetBps   float64       // minimum desired throughput
	SampleEvery time.Duration // measurement interval, cosmetic for tests
}

// DefaultAdaptOptions returns the documented defaults.
func DefaultAdaptOptions() AdaptOptions {
	return AdaptOptions{
		Window: 5, MinParallel: 1, MaxParallel: 32,
		MinQueue: 1, MaxQueue: 1024, TargetBps: 2_000_000,
	}
}

// Validate rejects impossible controller bounds.
func (o AdaptOptions) Validate() error {
	switch {
	case o.Window < 1 || o.Window > 256:
		return media.NewError(media.ClassInvalidArgument, "adapt", 0, "window %d outside 1..256", o.Window)
	case o.MinParallel < 1 || o.MaxParallel < o.MinParallel || o.MaxParallel > 4096:
		return media.NewError(media.ClassInvalidArgument, "adapt", 0, "invalid parallelism bounds %d..%d", o.MinParallel, o.MaxParallel)
	case o.MinQueue < 1 || o.MaxQueue < o.MinQueue || o.MaxQueue > 1<<20:
		return media.NewError(media.ClassInvalidArgument, "adapt", 0, "invalid queue bounds %d..%d", o.MinQueue, o.MaxQueue)
	case o.TargetBps <= 0:
		return media.NewError(media.ClassInvalidArgument, "adapt", 0, "target bps must be positive")
	}
	return nil
}

// Controller tracks throughput over a sliding window and derives bounded
// concurrency and queue limits. Larger throughput raises concurrency, drops
// lower it, and every value is clamped to the configured range.
type Controller struct {
	opts     AdaptOptions
	samples  []BandwidthSample
	parallel int
	queue    int
	inWindow int
	bytes    int
	packets  int
	start    time.Time
	total    int
}

// NewController builds a controller at the minimum settings; it ramps up as
// samples accumulate.
func NewController(opts AdaptOptions) (*Controller, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	return &Controller{opts: opts, parallel: opts.MinParallel, queue: opts.MinQueue}, nil
}

// Observe records bytes received for a packet at time t. It returns true when a
// window sample was completed and the limits may have changed.
func (c *Controller) Observe(t time.Time, bytes int) bool {
	return c.ObservePackets(t, bytes, 1)
}

// ObservePackets is Observe with an explicit packet count, so a caller that
// already knows how many datagrams a byte total covers can record it directly.
func (c *Controller) ObservePackets(t time.Time, bytes, packets int) bool {
	if bytes < 0 {
		bytes = 0
	}
	if packets < 0 {
		packets = 0
	}
	if c.start.IsZero() {
		c.start = t
	}
	c.inWindow += bytes
	c.bytes += bytes
	c.packets += packets
	c.total += bytes
	if t.Sub(c.start) < c.opts.SampleEvery {
		return false
	}
	elapsed := t.Sub(c.start)
	sample := BandwidthSample{
		Start: c.start, End: t, Bytes: c.inWindow, Packets: c.packets,
		Bitrate: float64(c.inWindow*8) / elapsed.Seconds(),
		Window:  len(c.samples) + 1,
	}
	c.samples = append(c.samples, sample)
	if len(c.samples) > c.opts.Window {
		c.samples = c.samples[len(c.samples)-c.opts.Window:]
	}
	c.inWindow, c.packets, c.start = 0, 0, time.Time{}
	// flagged sample: mark whether the derived window was clamped
	clamped := false
	w := c.desiredParallel()
	if w == c.opts.MinParallel || w == c.opts.MaxParallel {
		clamped = true
	}
	c.samples[len(c.samples)-1].Clamped = clamped
	c.parallel = w
	c.queue = c.desiredQueue()
	return true
}

// meanBps averages the sliding window, weighting each sample equally.
func (c *Controller) meanBps() float64 {
	if len(c.samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range c.samples {
		sum += s.Bitrate
	}
	return sum / float64(len(c.samples))
}

func (c *Controller) desiredParallel() int {
	if len(c.samples) == 0 {
		return c.opts.MinParallel
	}
	mean := c.meanBps()
	if mean <= 0 {
		return c.opts.MinParallel
	}
	// Scale linearly against the target: throughput at the target maps to the
	// configured maximum, lower throughput scales down proportionally. This
	// keeps the entire configured range reachable while never exceeding it.
	ratio := mean / c.opts.TargetBps
	span := float64(c.opts.MaxParallel - c.opts.MinParallel)
	want := c.opts.MinParallel + int(math.Round(span*ratio))
	return clampInt(want, c.opts.MinParallel, c.opts.MaxParallel)
}

func (c *Controller) desiredQueue() int {
	// queue grows with concurrency so slow workers apply backpressure instead
	// of dropping, but stays bounded.
	q := c.parallel * 2
	return clampInt(q, c.opts.MinQueue, c.opts.MaxQueue)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Limits returns the current concurrency and queue limits.
func (c *Controller) Limits() (parallel, queue int) { return c.parallel, c.queue }

// Reset clears measurements and returns the limits to their floors. It keeps
// the configured bounds so a live stream can ramp up from zero again.
func (c *Controller) Reset() {
	c.samples = nil
	c.inWindow, c.packets, c.bytes, c.total = 0, 0, 0, 0
	c.start = time.Time{}
	c.parallel = c.opts.MinParallel
	c.queue = c.opts.MinQueue
}

// Parallel returns the current concurrency limit.
func (c *Controller) Parallel() int { return c.parallel }

// Queue returns the current queue capacity.
func (c *Controller) Queue() int { return c.queue }

// Samples returns a copy of the completed bandwidth samples.
func (c *Controller) Samples() []BandwidthSample {
	return append([]BandwidthSample(nil), c.samples...)
}

// Summary reports min/mean/max bitrate over all retained samples.
func (c *Controller) Summary() (min, mean, max float64) {
	if len(c.samples) == 0 {
		return 0, 0, 0
	}
	min, max = math.Inf(1), 0
	for _, s := range c.samples {
		if s.Bitrate < min {
			min = s.Bitrate
		}
		if s.Bitrate > max {
			max = s.Bitrate
		}
	}
	return min, c.meanBps(), max
}

// SortSamples orders samples by their end time; used for stable reporting.
func SortSamples(in []BandwidthSample) []BandwidthSample {
	out := append([]BandwidthSample(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].End.Before(out[j].End) })
	return out
}
