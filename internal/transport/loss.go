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
	// LossPending means the interval has been declared but may still be
	// repaired before its fragment is committed.
	LossPending LossState = "pending"
	// LossLost is a committed hole; media inside it is absent from output.
	LossLost LossState = "lost"
	// LossRecovered means a repair packet supplied the missing bytes before
	// the owning fragment was committed.
	LossRecovered LossState = "recovered"
)

// LossInterval is one contiguous burst of missing sequence numbers.
type LossInterval struct {
	Stream int       `json:"stream"`
	First  int64     `json:"first"`
	Last   int64     `json:"last"`
	State  LossState `json:"state"`
	// Cause is a short machine-readable reason such as "burst" or "repair".
	Cause string `json:"cause"`
}

// Count is the number of sequence numbers in the interval.
func (l LossInterval) Count() int64 {
	if l.Last < l.First {
		return 0
	}
	return l.Last - l.First + 1
}

// LossRecorder accumulates loss intervals, merges adjacent bursts, applies
// repairs, and records late arrivals that could not be applied.
type LossRecorder struct {
	stream int
	max    int
	items  []LossInterval
	late   []merr.Span
	errs   []*merr.Error
}

// NewLossRecorder returns a recorder bounded to max intervals.
func NewLossRecorder(stream, max int) *LossRecorder {
	if max <= 0 {
		max = 1024
	}
	return &LossRecorder{stream: stream, max: max}
}

// Record adds an observed gap and declares it pending. Adjacent gaps merge.
func (l *LossRecorder) Record(first, last int64) {
	if last < first {
		return
	}
	idx := l.findOverlapping(first, last)
	if idx >= 0 {
		it := &l.items[idx]
		if it.State == LossRecovered {
			// A previously repaired interval cannot regress; a new gap that
			// touches it is a separate interval.
			idx = -1
		} else {
			if first < it.First {
				it.First = first
			}
			if last > it.Last {
				it.Last = last
			}
			return
		}
	}
	if len(l.items) >= l.max {
		if len(l.errs) < l.max {
			l.errs = append(l.errs, merr.New(merr.StageLoss, "loss_limit",
				merr.Span{Stream: l.stream, Seq: first, Offset: -1, Length: last - first + 1}, true,
				"loss interval limit %d reached", l.max))
		}
		return
	}
	l.items = append(l.items, LossInterval{
		Stream: l.stream, First: first, Last: last, State: LossPending, Cause: "burst",
	})
	l.sort()
}

// findOverlapping returns the index of an interval overlapping or touching
// [first,last], or -1.
func (l *LossRecorder) findOverlapping(first, last int64) int {
	for i := range l.items {
		if l.items[i].First <= last+1 && first <= l.items[i].Last+1 {
			return i
		}
	}
	return -1
}

func (l *LossRecorder) sort() {
	sort.SliceStable(l.items, func(i, j int) bool {
		if l.items[i].First != l.items[j].First {
			return l.items[i].First < l.items[j].First
		}
		return l.items[i].Last < l.items[j].Last
	})
}

// Recover marks a sequence number as repaired. It splits the interval when the
// repair is interior, so recovery evidence stays exact.
func (l *LossRecorder) Recover(seq int64) bool {
	for i := range l.items {
		it := &l.items[i]
		if seq < it.First || seq > it.Last || it.State == LossRecovered {
			continue
		}
		before := LossInterval{Stream: it.Stream, First: it.First, Last: seq - 1, State: LossRecovered, Cause: "repair"}
		after := LossInterval{Stream: it.Stream, First: seq + 1, Last: it.Last, State: LossRecovered, Cause: "repair"}
		replacement := make([]LossInterval, 0, 2)
		if before.Count() > 0 {
			replacement = append(replacement, before)
		}
		if after.Count() > 0 {
			replacement = append(replacement, after)
		}
		l.items = append(l.items[:i], append(replacement, l.items[i+1:]...)...)
		l.sort()
		return true
	}
	return false
}

// Commit finalizes every still-pending interval as lost. Call after flush.
func (l *LossRecorder) Commit() {
	for i := range l.items {
		if l.items[i].State == LossPending {
			l.items[i].State = LossLost
		}
	}
}

// RecordLate records a packet that arrived below the commit watermark. Late
// arrivals never alter recovered media, only the evidence trail.
func (l *LossRecorder) RecordLate(seq int64) {
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

// Counts returns (lost sequences, recovered sequences, pending sequences).
func (l *LossRecorder) Counts() (lost, recovered, pending int64) {
	for _, it := range l.items {
		switch it.State {
		case LossLost:
			lost += it.Count()
		case LossRecovered:
			recovered += it.Count()
		case LossPending:
			pending += it.Count()
		}
	}
	return
}

// BurstCount counts contiguous loss intervals that are not recovered.
func (l *LossRecorder) BurstCount() int {
	n := 0
	for _, it := range l.items {
		if it.State == LossLost {
			n++
		}
	}
	return n
}

// RecoveredBursts counts repaired intervals.
func (l *LossRecorder) RecoveredBursts() int {
	n := 0
	for _, it := range l.items {
		if it.State == LossRecovered {
			n++
		}
	}
	return n
}

// EstimatorConfig configures the sliding-window bandwidth estimator.
type EstimatorConfig struct {
	// Window is the wall-clock span of samples considered.
	Window time.Duration
	// MinSamples is the floor below which the estimate is reported as zero.
	MinSamples int
	// MaxRateBps caps absurd estimates from corrupt input.
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

// Estimator computes a sliding-window bitrate from committed packets. The
// estimate influences only buffering capacity; it never touches media
// timestamps.
type Estimator struct {
	cfg     EstimatorConfig
	clock   *timeline.Clock
	samples []bwSample
	bytes   int64
}

// NewEstimator validates cfg and binds it to the stream clock.
func NewEstimator(clock *timeline.Clock, cfg EstimatorConfig) (*Estimator, error) {
	if clock == nil {
		return nil, merr.New(merr.StageTransport, "nil_clock", merr.NoSpan(0), false, "estimator needs a clock")
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

// Add records a committed packet at the given absolute tick.
func (e *Estimator) Add(tick int64, size int) {
	if size < 0 {
		return
	}
	e.samples = append(e.samples, bwSample{tick: tick, bytes: int64(size)})
	e.bytes += int64(size)
	e.expire(tick)
}

// expire drops samples older than the window relative to now.
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

// RateBps returns the estimated bitrate in bits per second. It is exact
// integer arithmetic: bytes*8 scaled by the sample span.
func (e *Estimator) RateBps(now int64) int64 {
	if len(e.samples) < e.cfg.MinSamples {
		return 0
	}
	first := e.samples[0].tick
	spanTicks := now - first
	if spanTicks <= 0 {
		// All samples share a tick; use the window to avoid divide by zero.
		spanTicks = e.clock.FromNS(int64(e.cfg.Window))
	}
	if spanTicks <= 0 {
		return 0
	}
	// rate = bytes*8 * timescale / spanTicks
	bitsPerSec, err := timeline.Scale(e.bytes*8, e.clock.Timescale, e.clock.Timescale)
	if err != nil {
		return 0
	}
	scaled, err := scaleDiv(bitsPerSec, int64(e.clock.Timescale), spanTicks)
	if err != nil {
		return 0
	}
	if scaled > e.cfg.MaxRateBps {
		return e.cfg.MaxRateBps
	}
	return scaled
}

// scaleDiv computes x*y/d with overflow detection for non-negative operands.
func scaleDiv(x, y, d int64) (int64, error) {
	if d <= 0 {
		return 0, merr.New(merr.StageTransport, "bad_divisor", merr.NoSpan(0), false, "divisor %d", d)
	}
	const limit = int64(1) << 62
	if y != 0 && x > limit/y {
		// Fall back to a two-step division; the estimator tolerates floor.
		x /= d
		return x * y, nil
	}
	return (x * y) / d, nil
}

// BufferBytes converts the current estimate into a byte budget for a given
// buffering duration. This is the only consumer of the estimate.
func (e *Estimator) BufferBytes(now int64, hold time.Duration) int64 {
	rate := e.RateBps(now)
	if rate <= 0 || hold <= 0 {
		return 0
	}
	return rate / 8 * int64(hold/time.Second)
}

// FragmentReport is the per-fragment transport evidence.
type FragmentReport struct {
	Index      int   `json:"index"`
	Packets    int   `json:"packets"`
	Bytes      int64 `json:"bytes"`
	Lost       int64 `json:"lost"`
	RateBps    int64 `json:"rate_bps"`
	BufferCap  int64 `json:"buffer_cap_bytes"`
	StartTick  int64 `json:"start_tick"`
	EndTick    int64 `json:"end_tick"`
}

// Fragmentize assigns committed packets to fixed tick segments and reports
// bytes, loss and the estimate observed at the segment end. The estimate only
// drives BufferCap.
func Fragmentize(clock *timeline.Clock, packets []*Packet, tickOf func(*Packet) int64,
	losses []SeqSpan, origin, segmentTicks int64, est *Estimator, hold time.Duration) []FragmentReport {
	if clock == nil || segmentTicks <= 0 {
		return nil
	}
	bySeg := map[int]*FragmentReport{}
	var segs []int
	for _, p := range packets {
		idx := int(timeline.SegmentIndex(tickOf(p), origin, segmentTicks, timeline.EdgeOwnsRight))
		rep, ok := bySeg[idx]
		if !ok {
			rep = &FragmentReport{Index: idx}
			bySeg[idx] = rep
			segs = append(segs, idx)
		}
		rep.Packets++
		rep.Bytes += int64(len(p.Raw))
		tick := tickOf(p)
		if rep.StartTick == 0 || tick < rep.StartTick {
			rep.StartTick = tick
		}
		if tick > rep.EndTick {
			rep.EndTick = tick
		}
		if est != nil {
			est.Add(tick, len(p.Raw))
		}
	}
	for _, g := range losses {
		// Attribute a loss to the segment containing its middle tick when a
		// timestamp is unavailable: sequence-domain losses are attributed to
		// the segment owning the previous packet in the same stream.
		idx := lossSegment(packets, tickOf, g.First, origin, segmentTicks)
		rep, ok := bySeg[idx]
		if !ok {
			continue
		}
		rep.Lost += g.Count()
	}
	sort.Ints(segs)
	out := make([]FragmentReport, 0, len(segs))
	for _, idx := range segs {
		rep := bySeg[idx]
		rep.RateBps = est.RateBps(rep.EndTick)
		rep.BufferCap = est.BufferBytes(rep.EndTick, hold)
		out = append(out, *rep)
	}
	return out
}

// lossSegment locates the segment of the packet immediately preceding a gap.
func lossSegment(packets []*Packet, tickOf func(*Packet) int64, first int64,
	origin, segmentTicks int64) int {
	for i := len(packets) - 1; i >= 0; i-- {
		if int64(packets[i].Seq) < first {
			return int(timeline.SegmentIndex(tickOf(packets[i]), origin, segmentTicks, timeline.EdgeOwnsRight))
		}
	}
	return 0
}
