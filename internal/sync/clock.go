// Package sync fans per-representation RTP recovery results out to independent
// processing lanes and fans them back into a shared clock. The clock enforces
// a single nanosecond timeline: DASH periods and HLS discontinuity sequences
// are laid out contiguously, dialogue defines each period's anchor, and
// subtitle/sound-effect tracks are re-mapped onto that anchor at every cut so
// audio-visual sync is restored after any timeline jump.
package sync

import (
	"sort"

	"mediasync/internal/model"
)

// Cut marks one point where the shared timeline was severed by a
// discontinuity or boundary error, together with the track re-alignments.
type Cut struct {
	AtNS       int64
	PeriodID   int
	Reason     model.ErrorKind
	Alignments []Alignment
}

// Alignment records how far one non-dialogue track was shifted at a cut.
type Alignment struct {
	Representation string
	Kind           model.AssetKind
	ShiftNS        int64
}

// Clock is the single timeline all representations converge on.
type Clock struct {
	cuts        []Cut
	periodStart map[int]int64 // clock position of local-time zero per period
	periodSpan  map[int]int64 // longest local end observed per period
	aligned     map[string]bool
	lastPeriod  map[string]int // last period ingested per lane (FanIn calls)
}

// NewClock creates an empty shared clock.
func NewClock() *Clock {
	return &Clock{
		periodStart: map[int]int64{},
		periodSpan:  map[int]int64{},
		aligned:     map[string]bool{},
		lastPeriod:  map[string]int{},
	}
}

// laneKey identifies a FanIn lane by its representation name.
func laneKey(lane []model.SegmentDescriptor) string {
	if len(lane) == 0 {
		return ""
	}
	return lane[0].Representation
}

// layOut assigns a contiguous clock offset to each period the first time it is
// seen: period p starts exactly where period p-1 ended on the shared clock.
func (c *Clock) layOut(period int) {
	if _, ok := c.periodStart[period]; ok {
		return
	}
	if period == 0 {
		c.periodStart[0] = 0
		c.periodSpan[0] = 0
		return
	}
	c.layOut(period - 1)
	start := c.periodStart[period-1] + c.periodSpan[period-1]
	c.periodStart[period] = start
	c.periodSpan[period] = 0
}

// Ingest aligns one recovered segment onto the shared clock. The segment's
// local start (in its own timescale) is re-expressed in nanoseconds and offset
// by the period's clock position. Dialogue segments define the period span;
// subtitle/SFX shifts are recorded as alignments. The returned descriptor
// carries the final clock-aligned timing.
func (c *Clock) Ingest(s model.SegmentDescriptor) model.SegmentDescriptor {
	c.layOut(s.PeriodID)
	localStart := toLocalNS(s)
	clockStart := c.periodStart[s.PeriodID] + localStart

	out := s
	if s.Kind == model.KindSubtitle || s.Kind == model.KindSFX {
		shift := clockStart - s.StartNS
		if shift != 0 && !c.aligned[s.Representation] {
			c.recordAlignment(s.PeriodID, Alignment{
				Representation: s.Representation, Kind: s.Kind, ShiftNS: shift,
			})
			c.aligned[s.Representation] = true
		}
	}
	out.StartNS = clockStart
	if end := localStart + s.DurationNS; end > c.periodSpan[s.PeriodID] {
		c.periodSpan[s.PeriodID] = end
	}
	return out
}

// OpenCut records an externally detected timeline jump (discontinuity or
// boundary error) and returns the cut for reporting.
func (c *Clock) OpenCut(atNS int64, period int, reason model.ErrorKind) Cut {
	cut := Cut{AtNS: atNS, PeriodID: period, Reason: reason}
	c.cuts = append(c.cuts, cut)
	return cut
}

func (c *Clock) recordAlignment(period int, a Alignment) {
	if len(c.cuts) == 0 || c.cuts[len(c.cuts)-1].PeriodID != period {
		c.cuts = append(c.cuts, Cut{PeriodID: period, Reason: model.KindDiscontinuity})
	}
	idx := len(c.cuts) - 1
	c.cuts[idx].Alignments = append(c.cuts[idx].Alignments, a)
}

// Cuts returns all recorded timeline cuts in deterministic order.
func (c *Clock) Cuts() []Cut {
	out := make([]Cut, len(c.cuts))
	copy(out, c.cuts)
	return out
}

// FanIn ingests one representation lane, opening a cut whenever the period
// identifier advances, then returns the clock-aligned descriptors sorted by
// period, clock start and asset kind for deterministic downstream hashing.
func (c *Clock) FanIn(lane []model.SegmentDescriptor) []model.SegmentDescriptor {
	key := laneKey(lane)
	out := make([]model.SegmentDescriptor, 0, len(lane))
	lastPeriod, seen := c.lastPeriod[key]
	for _, s := range lane {
		if seen && s.PeriodID != lastPeriod {
			c.OpenCut(s.StartNS, s.PeriodID, model.KindDiscontinuity)
		}
		lastPeriod = s.PeriodID
		seen = true
		out = append(out, c.Ingest(s))
	}
	c.lastPeriod[key] = lastPeriod
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].PeriodID != out[j].PeriodID {
			return out[i].PeriodID < out[j].PeriodID
		}
		if out[i].StartNS != out[j].StartNS {
			return out[i].StartNS < out[j].StartNS
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// toLocalNS converts a segment's scale-local start into nanoseconds, falling
// back to its already-mapped StartNS if the scale data is unavailable.
func toLocalNS(s model.SegmentDescriptor) int64 {
	if s.Timescale == 0 {
		return s.StartNS
	}
	ns, err := model.ScaleToNanoseconds(s.StartInScale, s.Timescale)
	if err != nil {
		return s.StartNS
	}
	return ns
}
