package transport

import (
	"sort"
	"time"

	"mediadist/internal/merr"
	"mediadist/internal/timeline"
)

// LossState is the lifecycle of one loss interval.
type LossState string

const (
	// LossPending means the hole is declared but the owning fragment has not
	// crossed the commit horizon, so a repair can still change the outcome.
	LossPending LossState = "pending"
	// LossLost is a committed hole: media inside it is absent from output.
	LossLost LossState = "lost"
)

// LossInterval is one contiguous burst of missing sequence numbers.
type LossInterval struct {
	Stream int       `json:"stream"`
	First  int64     `json:"first"`
	Last   int64     `json:"last"`
	State  LossState `json:"state"`
	// Cause records how the interval was produced.
	Cause string `json:"cause"`
}

// Count is the number of sequence numbers in the interval.
func (l LossInterval) Count() int64 {
	if l.Last < l.First {
		return 0
	}
	return l.Last - l.First + 1
}

// LossRecorder is the single source of truth for missing-sequence evidence.
// Intervals are disjoint and sorted. Declared holes start pending; they become
// lost when the commit horizon passes them, unless a repair removes them
// first. A repair arriving after commit is refused and surfaces as a late
// packet instead.
type LossRecorder struct {
	stream    int
	max       int
	items     []LossInterval
	recovered int64 // sequence numbers repaired before commit
	bursts    int   // fully repaired intervals
	late      []merr.Span
	errs      []*merr.Error
}

// NewLossRecorder returns a recorder bounded to max intervals.
func NewLossRecorder(stream, max int) *LossRecorder {
	if max <= 0 {
		max = 1024
	}
	return &LossRecorder{stream: stream, max: max}
}

// Record declares a missing range. Touching or overlapping ranges merge, and a
// merged interval keeps the stricter state: lost dominates pending.
func (l *LossRecorder) Record(first, last int64) {
	if last < first {
		return
	}
	merged := LossInterval{Stream: l.stream, First: first, Last: last, State: LossPending, Cause: "burst"}
	var rest []LossInterval
	for _, it := range l.items {
		if it.First <= merged.Last+1 && merged.First <= it.Last+1 {
			if it.First < merged.First {
				merged.First = it.First
			}
			if it.Last > merged.Last {
				merged.Last = it.Last
			}
			if it.State == LossLost {
				merged.State = LossLost
			}
			if it.Cause == "partial_repair" {
				merged.Cause = "partial_repair"
			}
			continue
		}
		rest = append(rest, it)
	}
	rest = append(rest, merged)
	sort.SliceStable(rest, func(i, j int) bool {
		if rest[i].First != rest[j].First {
			return rest[i].First < rest[j].First
		}
		return rest[i].Last < rest[j].Last
	})
	if len(rest) > l.max {
		if len(l.errs) < l.max {
			l.errs = append(l.errs, merr.New(merr.StageLoss, "loss_limit",
				merr.Span{Stream: l.stream, Seq: first, Offset: -1, Length: last - first + 1}, true,
				"loss interval limit %d reached", l.max))
		}
		rest = rest[:l.max]
	}
	l.items = rest
}

// Recover marks seq as supplied by a repair packet. It returns false when the
// hole is unknown or already committed, in which case the packet is late.
func (l *LossRecorder) Recover(seq int64) bool {
	for i := range l.items {
		it := &l.items[i]
		if seq < it.First || seq > it.Last {
			continue
		}
		if it.State != LossPending {
			l.RecordLateSeq(seq)
			return false
		}
		l.recovered++
		if it.Count() == 1 {
			l.items = append(l.items[:i], l.items[i+1:]...)
			l.bursts++
			return true
		}
		before := LossInterval{Stream: it.Stream, First: it.First, Last: seq - 1, State: LossPending, Cause: it.Cause}
		after := LossInterval{Stream: it.Stream, First: seq + 1, Last: it.Last, State: LossPending, Cause: "partial_repair"}
		var replacement []LossInterval
		if before.Count() > 0 {
			replacement = append(replacement, before)
		}
		if after.Count() > 0 {
			replacement = append(replacement, after)
		}
		l.items = append(l.items[:i], append(replacement, l.items[i+1:]...)...)
		return true
	}
	return false
}

// CommitUpTo finalizes pending intervals ending at or before seq. Intervals
// straddling the horizon are split so the committed prefix is exact.
func (l *LossRecorder) CommitUpTo(seq int64) {
	var out []LossInterval
	for _, it := range l.items {
		if it.State != LossPending || it.First > seq {
			out = append(out, it)
			continue
		}
		if it.Last <= seq {
			it.State = LossLost
			out = append(out, it)
			continue
		}
		out = append(out,
			LossInterval{Stream: it.Stream, First: it.First, Last: seq, State: LossLost, Cause: it.Cause},
			LossInterval{Stream: it.Stream, First: seq + 1, Last: it.Last, State: LossPending, Cause: it.Cause},
		)
	}
	l.items = out
}

// Commit finalizes every still-pending interval as lost.
func (l *LossRecorder) Commit() { l.CommitUpTo(1<<62 - 1) }

// RecordLateSeq records a late sequence without a raw packet sequence.
func (l *LossRecorder) RecordLateSeq(seq int64) {
	if len(l.late) < l.max {
		l.late = append(l.late, merr.Span{Stream: l.stream, Seq: seq, Offset: -1})
	}
	if len(l.errs) < l.max {
		l.errs = append(l.errs, merr.New(merr.StageLoss, "late_arrival",
			merr.SeqSpan(l.stream, uint16(seq)), true,
			"late packet %d recorded outside committed fragments", seq))
	}
}

// Intervals returns the recorded intervals in ascending order.
func (l *LossRecorder) Intervals() []LossInterval {
	return append([]LossInterval(nil), l.items...)
}

// Late returns the late-arrival spans in arrival order.
func (l *LossRecorder) Late() []merr.Span { return append([]merr.Span(nil), l.late...) }

// Errors returns recoverable loss-side errors.
func (l *LossRecorder) Errors() []*merr.Error { return append([]*merr.Error(nil), l.errs...) }

// Counts returns (lost, recovered, pending) sequence counts.
func (l *LossRecorder) Counts() (lost, recovered, pending int64) {
	for _, it := range l.items {
		switch it.State {
		case LossLost:
			lost += it.Count()
		case LossPending:
			pending += it.Count()
		}
	}
	return lost, l.recovered, pending
}

// LostBursts counts committed loss intervals.
func (l *LossRecorder) LostBursts() int {
	n := 0
	for _, it := range l.items {
		if it.State == LossLost {
			n++
		}
	}
	return n
}

// RecoveredBursts counts intervals fully removed by repair.
func (l *LossRecorder) RecoveredBursts() int { return l.bursts }

// EstimatorConfig configures the sliding-window bandwidth estimator.
type EstimatorConfig struct {
	Window     time.Duration
	MinSamples int
	MaxRateBps int64
}

// DefaultEstimatorConfig uses a two-second window.
func DefaultEstimatorConfig() EstimatorConfig {
	return EstimatorConfig{Window: 2 * time.Second, MinSamples: 2, MaxRateBps: 1 << 30}
}

type bwSample struct {
	tick  int64
	bytes int64
}

// Estimator computes a sliding-window bitrate from committed samples. The
// estimate influences buffer capacity only; it never touches media timestamps.
type Estimator struct {
	cfg     EstimatorConfig
	clock   *timeline.Clock
	samples []bwSample
	bytes   int64
}

// NewEstimator validates cfg and binds it to the stream clock.
func NewEstimator(clock *timeline.Clock, cfg EstimatorConfig) (*Estimator, error) {
	if clock == nil {
		return nil, merr.New(merr.StageTransport, "nil_clock", merr.NoSpan(0), false,
			"estimator needs a clock")
	}
	if cfg.Window <= 0 {
		cfg.Window = DefaultEstimatorConfig().Window
	}
	if cfg.MinSamples <= 0 {
		cfg.MinSamples = DefaultEstimatorConfig().MinSamples
	}
	if cfg.MaxRateBps <= 0 {
		cfg.MaxRateBps = DefaultEstimatorConfig().MaxRateBps
	}
	return &Estimator{cfg: cfg, clock: clock}, nil
}

// Add records a committed sample at the given absolute tick.
func (e *Estimator) Add(tick int64, size int) {
	if size < 0 {
		return
	}
	e.samples = append(e.samples, bwSample{tick: tick, bytes: int64(size)})
	e.bytes += int64(size)
	e.expire(tick)
}

func (e *Estimator) expire(now int64) {
	winTicks := e.clock.FromNS(int64(e.cfg.Window))
	cut := now - winTicks
	drop := 0
	for drop < len(e.samples) && e.samples[drop].tick < cut {
		e.bytes -= e.samples[drop].bytes
		drop++
	}
	if drop > 0 {
		e.samples = append(e.samples[:0], e.samples[drop:]...)
	}
}

// RateBps returns the estimated bitrate in bits per second, using exact
// integer arithmetic over the observed tick span.
func (e *Estimator) RateBps(now int64) int64 {
	if len(e.samples) < e.cfg.MinSamples {
		return 0
	}
	first := e.samples[0].tick
	spanTicks := now - first
	if spanTicks <= 0 {
		return 0
	}
	scaled, ok := safeMulDiv(e.bytes*8, int64(e.clock.Timescale), spanTicks)
	if !ok {
		return 0
	}
	if scaled > e.cfg.MaxRateBps {
		return e.cfg.MaxRateBps
	}
	return scaled
}

// safeMulDiv computes x*y/d for non-negative operands, splitting the
// multiplication when the product would overflow.
func safeMulDiv(x, y, d int64) (int64, bool) {
	if x < 0 || y < 0 || d <= 0 {
		return 0, false
	}
	const limit = int64(1) << 62
	if y != 0 && x > limit/y {
		x /= d
		if y != 0 && x > limit/y {
			return 0, false
		}
		return x * y, true
	}
	return (x * y) / d, true
}

// BufferBytes converts the estimate into a byte budget for holding media.
// This is the only consumer of the estimate.
func (e *Estimator) BufferBytes(now int64, hold time.Duration) int64 {
	rate := e.RateBps(now)
	if rate <= 0 || hold <= 0 {
		return 0
	}
	secs := int64(hold / time.Second)
	if secs <= 0 {
		secs = 1
	}
	return rate / 8 * secs
}

// FragmentSample is one committed sample's contribution to a segment report.
type FragmentSample struct {
	Segment  int
	Bytes    int64
	Missing  int64
	Tick     int64
	Keyframe bool
}

// FragmentReport is the per-fragment transport evidence.
type FragmentReport struct {
	Index     int   `json:"index"`
	Samples   int   `json:"samples"`
	Bytes     int64 `json:"bytes"`
	Lost      int64 `json:"lost"`
	Keyframes int   `json:"keyframes"`
	RateBps   int64 `json:"rate_bps"`
	BufferCap int64 `json:"buffer_cap_bytes"`
	StartTick int64 `json:"start_tick"`
	EndTick   int64 `json:"end_tick"`
}

// AggregateFragments builds per-segment reports from committed samples and the
// estimator's view at each segment end. Bandwidth affects only BufferCap.
func AggregateFragments(inputs []FragmentSample, est *Estimator, hold time.Duration) []FragmentReport {
	bySeg := map[int]*FragmentReport{}
	var order []int
	for _, in := range inputs {
		rep, ok := bySeg[in.Segment]
		if !ok {
			rep = &FragmentReport{Index: in.Segment, StartTick: in.Tick, EndTick: in.Tick}
			bySeg[in.Segment] = rep
			order = append(order, in.Segment)
		}
		rep.Samples++
		rep.Bytes += in.Bytes
		rep.Lost += in.Missing
		if in.Keyframe {
			rep.Keyframes++
		}
		if in.Tick < rep.StartTick {
			rep.StartTick = in.Tick
		}
		if in.Tick > rep.EndTick {
			rep.EndTick = in.Tick
		}
		if est != nil {
			est.Add(in.Tick, int(in.Bytes))
		}
	}
	sort.Ints(order)
	out := make([]FragmentReport, 0, len(order))
	for _, idx := range order {
		rep := bySeg[idx]
		rep.RateBps = est.RateBps(rep.EndTick)
		rep.BufferCap = est.BufferBytes(rep.EndTick, hold)
		out = append(out, *rep)
	}
	return out
}
