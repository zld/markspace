package timeline

import (
	"fmt"
	"strconv"
	"strings"

	"rtpregress/internal/media"
	"rtpregress/internal/planner"
)

// AlignedEvent is one event after cross-track alignment: its time is snapped
// to the plan's precision, its segment is resolved, and the original and
// snapped times are both kept so the verifier can measure the deviation.
type AlignedEvent struct {
	Event
	// QuantizedNs is TimeNs rounded to the plan's precision grid.
	QuantizedNs int64
	// DeviationNs is QuantizedNs - TimeNs.
	DeviationNs int64
	// Segment is the 0-based output segment containing QuantizedNs.
	Segment int
}

// Alignment is the whole aligned event set plus the deviation summary.
type Alignment struct {
	Events []AlignedEvent
	// MaxDeviationNs is the largest absolute quantization deviation.
	MaxDeviationNs int64
	// Segments is how many output segments the plan produces.
	Segments int
	// SegmentCounts is how many events fall in each segment.
	SegmentCounts []int
}

// Reference is the anchor time other tracks are expressed relative to. It is
// the earliest committed presentation time across all inputs.
type Reference struct {
	// Track is the anchor track name.
	Track string `json:"track"`
	// TimeNs is the anchor's earliest time.
	TimeNs int64 `json:"timeNs"`
}

// Canonicalize rescales each track's events onto the reference timeline.
// Tracks that already share the reference's clock are left untouched; tracks
// with a different timescale are converted through the exact timebase map
// rather than by subtracting tick counts directly. Events before the anchor
// are normalized by shifting the whole set so time starts at zero, which is
// what a generated artifact can represent.
func Canonicalize(events []Event, ref Reference) ([]Event, error) {
	if len(events) == 0 {
		return nil, nil
	}
	// Find the earliest event overall; it defines the zero of the artifact
	// timeline.
	min := events[0].TimeNs
	for _, e := range events[1:] {
		if e.TimeNs < min {
			min = e.TimeNs
		}
	}
	out := make([]Event, len(events))
	for i, e := range events {
		shifted := e.TimeNs - min
		if shifted < 0 {
			return nil, media.At(media.Newf(media.KindTimebase,
				"event time %dns precedes the timeline anchor %dns", e.TimeNs, min),
				media.Loc{Track: e.Track, Sample: e.Sample, Stage: "align",
					Offset: e.Offset, HasOffset: e.HasOffset})
		}
		e.TimeNs = shifted
		out[i] = e
	}
	_ = ref
	return out, nil
}

// Align snaps canonical events to the plan's precision and assigns segments.
// Events are sorted first, so the result is independent of input file order.
// An event that lands exactly on a segment boundary belongs to the later
// segment, matching the seg start <= t < end rule the verifier enforces.
func Align(events []Event, plan *planner.Plan) (*Alignment, error) {
	if plan == nil {
		return nil, media.New(media.KindBudget, "align: nil plan")
	}
	if plan.PrecisionUS <= 0 {
		return nil, media.Newf(media.KindBudget, "align: invalid precision %dus", plan.PrecisionUS)
	}
	SortEvents(events)
	if len(events) == 0 {
		return &Alignment{Segments: 1, SegmentCounts: []int{0}}, nil
	}
	segs := planner.SegmentCount(DurationOf(events), plan.SegmentMS)
	if segs < 1 {
		segs = 1
	}
	out := make([]AlignedEvent, 0, len(events))
	counts := make([]int, segs)
	var maxDev int64
	for _, e := range events {
		q := plan.QuantizeNs(e.TimeNs)
		dev := q - e.TimeNs
		if dev < 0 {
			dev = -dev
		}
		if dev > maxDev {
			maxDev = dev
		}
		seg := segmentIndex(e.TimeNs, plan.SegmentMS, segs)
		if seg >= segs {
			seg = segs - 1
		}
		counts[seg]++
		out = append(out, AlignedEvent{
			Event:       e,
			QuantizedNs: q,
			DeviationNs: q - e.TimeNs,
			Segment:     seg,
		})
	}
	return &Alignment{Events: out, MaxDeviationNs: maxDev, Segments: segs, SegmentCounts: counts}, nil
}

func segmentIndex(t int64, segMS, segs int) int {
	if segMS <= 0 {
		return 0
	}
	idx := int(t / (int64(segMS) * 1_000_000))
	if idx < 0 {
		return 0
	}
	if idx >= segs {
		return segs - 1
	}
	return idx
}

// Line renders one aligned event for the item-by-item input-to-output map.
// The order is fixed: time, segment, track, source, sample, bytes, text.
func (a AlignedEvent) Line(file string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "t=%s->%s seg=%d track=%s src=%s",
		nsText(a.TimeNs), nsText(a.QuantizedNs), a.Segment, a.Track, a.Source)
	if a.Sample > 0 {
		fmt.Fprintf(&b, " sample=%d", a.Sample)
	}
	if a.Scheme != "" {
		// Keep the scheme short: the UUID is 32 hex chars in SEI events.
		fmt.Fprintf(&b, " scheme=%s", shortScheme(a.Scheme))
	}
	fmt.Fprintf(&b, " bytes=%d text=%s", len(a.Text), sanitize(a.Text))
	if file != "" {
		fmt.Fprintf(&b, " file=%s", file)
	}
	return b.String()
}

func nsText(ns int64) string {
	// Render as milliseconds with three decimals; sufficient for the 1ms
	// acceptance threshold and stable across runs.
	neg := ""
	if ns < 0 {
		neg, ns = "-", -ns
	}
	ms := ns / 1_000_000
	us := (ns % 1_000_000) / 1000
	return neg + strconv.FormatInt(ms, 10) + "." + pad3(int(us)) + "ms"
}

func pad3(v int) string {
	s := strconv.Itoa(v)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}

func shortScheme(s string) string {
	if len(s) > 8 && !strings.ContainsAny(s, "-") {
		return s[:8]
	}
	return s
}

func sanitize(s string) string {
	r := strings.NewReplacer("\n", "\\n", "\r", "\\r", "\t", "\\t")
	return r.Replace(s)
}
