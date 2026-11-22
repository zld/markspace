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
	// LossPending means the hole is declared but not yet committed, so a
	// repair can still fill it.
	LossPending LossState = "pending"
	// LossLost is a committed hole: the fragment owning it was emitted
	// without these sequence numbers, so the media inside is absent.
	LossLost LossState = "lost"
	// LossRecovered marks sequence numbers a retransmission supplied before
	// the hole was committed. It is reported as evidence, not as an interval
	// still missing.
	LossRecovered LossState = "recovered"
)

// LossInterval is one contiguous run of missing or repaired sequence numbers.
type LossInterval struct {
	Stream int       `json:"stream"`
	First  int64     `json:"first"`
	Last   int64     `json:"last"`
	State  LossState `json:"state"`
}

// Count is the number of sequence numbers in the interval.
func (l LossInterval) Count() int64 {
	if l.Last < l.First {
		return 0
	}
	return l.Last - l.First + 1
}

// LossRecorder is the single source of truth for sequence-level gaps.
//
// The reorder buffer decides what is missing; this recorder only classifies
// it. Declared pending holes may still be repaired. When a hole is committed,
// its sequence range moves to lost evidence. A retransmission is accepted only
// for a sequence that is currently pending, so normal reordering, duplicates
// and bogus repairs can never masquerade as recovery.
type LossRecorder struct {
	stream   int
	max      int
	pending  []LossInterval
	lost     []LossInterval
	repaired map[int64]struct{}
	late     []merr.Span
	errs     []*merr.Error
}

// NewLossRecorder returns a recorder bounded to max intervals.
func NewLossRecorder(stream, max int) *LossRecorder {
	if max <= 0 {
		max = 1024
	}
	return &LossRecorder{stream: stream, max: max, repaired: make(map[int64]struct{})}
}

// RecordPending declares holes the reorder buffer is still holding back. They
// are merge-safe and can be declared repeatedly as the buffer advances.
func (l *LossRecorder) RecordPending(first, last int64) {
	if last < first {
		return
	}
	merged := LossInterval{Stream: l.stream, First: first, Last: last, State: LossPending}
	var out []LossInterval
	for _, cur := range l.pending {
		if cur.First <= merged.Last+1 && merged.First <= cur.Last+1 {
			if cur.First < merged.First {
				merged.First = cur.First
			}
			if cur.Last > merged.Last {
				merged.Last = cur.Last
			}
			continue
		}
		out = append(out, cur)
	}
	out = append(out, merged)
	l.pending = sortIntervals(out)
}

// RecordLost adds a committed gap. Any pending overlap is consumed, and a
// sequence already repaired is excluded from the lost evidence.
func (l *LossRecorder) RecordLost(first, last int64) {
	if last < first {
		return
	}
	l.removePending(first, last)
	parts, _ := splitAround(first, last, l.repaired)
	for _, iv := range parts {
		l.insertLost(iv)
	}
}

// removePending removes [first,last] from the pending set.
func (l *LossRecorder) removePending(first, last int64) {
	var out []LossInterval
	for _, cur := range l.pending {
		if cur.Last < first || cur.First > last {
			out = append(out, cur)
			continue
		}
		if cur.First < first {
			out = append(out, LossInterval{Stream: l.stream, First: cur.First, Last: first - 1, State: LossPending})
		}
		if cur.Last > last {
			out = append(out, LossInterval{Stream: l.stream, First: last + 1, Last: cur.Last, State: LossPending})
		}
	}
	l.pending = sortIntervals(out)
}

// splitAround removes repaired sequences from [first,last].
func splitAround(first, last int64, repaired map[int64]struct{}) ([]LossInterval, int) {
	var out []LossInterval
	cursor := first
	hits := 0
	for s := first; s <= last; s++ {
		if _, ok := repaired[s]; !ok {
			continue
		}
		hits++
		if s > cursor {
			out = append(out, LossInterval{First: cursor, Last: s - 1, State: LossLost})
		}
		cursor = s + 1
	}
	if cursor <= last {
		out = append(out, LossInterval{First: cursor, Last: last, State: LossLost})
	}
	return out, hits
}

func (l *LossRecorder) insertLost(iv LossInterval) {
	iv.Stream = l.stream
	iv.State = LossLost
	out := l.lost[:0]
	merged := iv
	for _, cur := range l.lost {
		if cur.First <= merged.Last+1 && merged.First <= cur.Last+1 {
			if cur.First < merged.First {
				merged.First = cur.First
			}
			if cur.Last > merged.Last {
				merged.Last = cur.Last
			}
			continue
		}
		out = append(out, cur)
	}
	out = append(out, merged)
	l.lost = sortIntervals(out)
	if len(l.lost) > l.max {
		l.lost = l.lost[:l.max]
	}
}

func sortIntervals(in []LossInterval) []LossInterval {
	sort.SliceStable(in, func(i, j int) bool {
		if in[i].First != in[j].First {
			return in[i].First < in[j].First
		}
		return in[i].Last < in[j].Last
	})
	return in
}

// Recover records a retransmission for a currently pending sequence. It
// returns false when the sequence is not pending (already committed, a
// duplicate, or never declared), so the caller can classify the packet.
func (l *LossRecorder) Recover(seq int64) bool {
	idx := -1
	for i := range l.pending {
		if seq >= l.pending[i].First && seq <= l.pending[i].Last {
			idx = i
			break
		}
	}
	if idx < 0 {
		// Already lost: this is a late arrival.
		for _, iv := range l.lost {
			if seq >= iv.First && seq <= iv.Last {
				l.RecordLateSeq(seq)
				return false
			}
		}
		return false
	}
	cur := l.pending[idx]
	var replacement []LossInterval
	if seq > cur.First {
		replacement = append(replacement, LossInterval{Stream: l.stream, First: cur.First, Last: seq - 1, State: LossPending})
	}
	if seq < cur.Last {
		replacement = append(replacement, LossInterval{Stream: l.stream, First: seq + 1, Last: cur.Last, State: LossPending})
	}
	next := append(append([]LossInterval(nil), l.pending[:idx]...), replacement...)
	next = append(next, l.pending[idx+1:]...)
	l.pending = next
	l.repaired[seq] = struct{}{}
	return true
}

// RecoveredBursts counts contiguous runs of repaired sequence numbers.
func (l *LossRecorder) RecoveredBursts() int {
	if len(l.repaired) == 0 {
		return 0
	}
	seqs := make([]int64, 0, len(l.repaired))
	for s := range l.repaired {
		seqs = append(seqs, s)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	runs := 1
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			runs++
		}
	}
	return runs
}

// RecordLateSeq records a sequence that could not be applied.
func (l *LossRecorder) RecordLateSeq(seq int64) {
	if len(l.late) < l.max {
		l.late = append(l.late, merr.Span{Stream: l.stream, Seq: seq, Offset: -1})
	}
	if len(l.errs) < l.max {
		l.errs = append(l.errs, merr.New(merr.StageLoss, "late_arrival",
			merr.SeqSpan(l.stream, uint16(seq)), true,
			"late packet %d arrived after its fragment committed", seq))
	}
}

// Intervals returns lost intervals first, then recovered runs, each ascending
// and each run merged, so reports are stable.
func (l *LossRecorder) Intervals() []LossInterval {
	out := append([]LossInterval(nil), l.lost...)
	if len(l.repaired) > 0 {
		seqs := make([]int64, 0, len(l.repaired))
		for s := range l.repaired {
			seqs = append(seqs, s)
		}
		sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
		first, prev := seqs[0], seqs[0]
		for _, s := range seqs[1:] {
			if s == prev+1 {
				prev = s
				continue
			}
			out = append(out, LossInterval{Stream: l.stream, First: first, Last: prev, State: LossRecovered})
			first, prev = s, s
		}
		out = append(out, LossInterval{Stream: l.stream, First: first, Last: prev, State: LossRecovered})
	}
	return out
}

// Late returns late-arrival spans in arrival order.
func (l *LossRecorder) Late() []merr.Span { return append([]merr.Span(nil), l.late...) }

// Errors returns recoverable loss-side errors.
func (l *LossRecorder) Errors() []*merr.Error { return append([]*merr.Error(nil), l.errs...) }

// Counts returns (lost sequences, recovered sequences).
func (l *LossRecorder) Counts() (lost, recovered int64) {
	for _, iv := range l.lost {
		lost += iv.Count()
	}
	return lost, int64(len(l.repaired))
}

// LostBursts counts committed loss intervals.
func (l *LossRecorder) LostBursts() int { return len(l.lost) }

// PendingCount returns how many sequences are still repairable.
func (l *LossRecorder) PendingCount() int64 {
	var n int64
	for _, iv := range l.pending {
		n += iv.Count()
	}
	return n
}

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
