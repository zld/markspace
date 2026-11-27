package clock

import (
	"math"
	"sort"

	"tracecheck/internal/serr"
)

// Anchor binds one raw client clock reading to a position on the media
// timeline. At least one anchor is required before any event can be mapped.
type Anchor struct {
	ClientNanos int64 // client clock reading already converted to nanoseconds
	MediaNanos  int64 // equivalent position on the media timeline
	Label       string
}

// Event is a client event together with its mapping onto the media timeline.
type Event struct {
	Index       int
	Source      string // event source, e.g. "client-log" or "rtp"
	Raw         int64  // raw client clock reading in its own unit
	ClientNanos int64  // Raw converted to nanoseconds
	MediaNanos  int64  // mapped position on the media timeline
	ErrorNanos  int64  // absolute error bound of the mapping
	AnchorUsed  int    // index of the nearest anchor in the mapper
}

// Mapper maps client event timestamps onto the media timeline using piecewise
// linear interpolation between sorted anchors and bounded extrapolation.
type Mapper struct {
	tb        Timebase
	anchors   []Anchor
	maxExtrap int64 // maximum allowed distance outside the anchor span
}

// DefaultMaxExtrapolation is how far outside the anchor span an event may fall
// before the mapping reports CodeNoAnchor (30 s).
const DefaultMaxExtrapolation int64 = 30 * NanosPerSecond

// ErrNoAnchor is returned when no anchor exists at all.
func ErrNoAnchor(source string) *serr.Error {
	return serr.New(serr.CodeNoAnchor, "%s: no mapping anchor available", source)
}

// NewMapper builds a mapper over the given anchors. Anchors are sorted by
// client time; duplicate client times are rejected as ambiguous. A nil or
// empty anchor slice is allowed but Map will then report CodeNoAnchor.
func NewMapper(tb Timebase, anchors []Anchor) (*Mapper, error) {
	if err := tb.Validate(); err != nil {
		return nil, err
	}
	sorted := append([]Anchor(nil), anchors...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].ClientNanos < sorted[j].ClientNanos
	})
	for i := 1; i < len(sorted); i++ {
		if sorted[i].ClientNanos == sorted[i-1].ClientNanos {
			return nil, serr.New(serr.CodeInvalidArgs,
				"two anchors share client time %d ns", sorted[i].ClientNanos)
		}
	}
	return &Mapper{tb: tb, anchors: sorted, maxExtrap: DefaultMaxExtrapolation}, nil
}

// SetMaxExtrapolation overrides the default extrapolation window; negative
// values disable the limit.
func (m *Mapper) SetMaxExtrapolation(ns int64) { m.maxExtrap = ns }

// Anchors returns a copy of the sorted anchors.
func (m *Mapper) Anchors() []Anchor { return append([]Anchor(nil), m.anchors...) }

// Map converts raw (in the timebase's client unit) to nanoseconds and places it
// on the media timeline. It records the original value, mapped value and the
// error bound. Conversion overflow and a missing/unusable anchor return the
// structured errors CodeClockOverflow and CodeNoAnchor.
func (m *Mapper) Map(raw int64, source string) (Event, error) {
	clientNs, err := m.tb.ClientToNanos(raw)
	if err != nil {
		return Event{}, err
	}
	return m.mapNanos(raw, clientNs, source)
}

// MapNanos maps an already converted client nanosecond reading.
func (m *Mapper) MapNanos(clientNs int64, source string) (Event, error) {
	return m.mapNanos(clientNs, clientNs, source)
}

func (m *Mapper) mapNanos(raw, clientNs int64, source string) (Event, error) {
	if len(m.anchors) == 0 {
		return Event{}, ErrNoAnchor(source)
	}
	ev := Event{Source: source, Raw: raw, ClientNanos: clientNs}
	idx := m.nearestAnchor(clientNs)
	ev.AnchorUsed = idx
	first := m.anchors[0]
	last := m.anchors[len(m.anchors)-1]

	if clientNs < first.ClientNanos {
		if m.maxExtrap >= 0 && first.ClientNanos-clientNs > m.maxExtrap {
			return Event{}, serr.New(serr.CodeNoAnchor,
				"%s: client time %d ns precedes the first anchor by more than %d ns",
				source, clientNs, m.maxExtrap)
		}
		hi := first
		if len(m.anchors) > 1 {
			hi = m.anchors[1]
		}
		ev.MediaNanos = extrapolate(clientNs, first, first, hi)
	} else if clientNs > last.ClientNanos {
		if m.maxExtrap >= 0 && clientNs-last.ClientNanos > m.maxExtrap {
			return Event{}, serr.New(serr.CodeNoAnchor,
				"%s: client time %d ns follows the last anchor by more than %d ns",
				source, clientNs, m.maxExtrap)
		}
		lo := last
		if n := len(m.anchors); n > 1 {
			lo = m.anchors[n-2]
		}
		ev.MediaNanos = extrapolate(clientNs, last, lo, last)
	} else {
		ev.MediaNanos = interpolate(clientNs, m.anchors)
	}
	// The uncertainty grows with distance from the nearest anchor because
	// between anchors the true client/media relation is unknown.
	ev.ErrorNanos = abs64(clientNs - m.anchors[idx].ClientNanos)
	return ev, nil
}

// nearestAnchor returns the index of the anchor closest in client time.
func (m *Mapper) nearestAnchor(clientNs int64) int {
	best, bestDist := 0, abs64(clientNs-m.anchors[0].ClientNanos)
	for i := 1; i < len(m.anchors); i++ {
		d := abs64(clientNs - m.anchors[i].ClientNanos)
		if d < bestDist {
			best, bestDist = i, d
		}
	}
	return best
}

// interpolate finds the bracketing anchors and linearly maps clientNs.
func interpolate(clientNs int64, anchors []Anchor) int64 {
	for i := 1; i < len(anchors); i++ {
		if clientNs <= anchors[i].ClientNanos {
			return extrapolate(clientNs, anchors[i], anchors[i-1], anchors[i])
		}
	}
	return anchors[len(anchors)-1].MediaNanos
}

// extrapolate maps clientNs using the slope between lo and hi, evaluated even
// outside [lo,hi]. All values are nanoseconds; the ratio is computed in
// float64 to avoid intermediate overflow, which is exact enough for a mapping
// error that is itself reported.
func extrapolate(clientNs int64, ref Anchor, lo, hi Anchor) int64 {
	span := float64(hi.ClientNanos - lo.ClientNanos)
	if span == 0 {
		return ref.MediaNanos
	}
	slope := float64(hi.MediaNanos-lo.MediaNanos) / span
	offset := float64(clientNs-ref.ClientNanos) * slope
	return ref.MediaNanos + int64(math.Round(offset))
}

func abs64(v int64) int64 {
	if v == math.MinInt64 {
		return math.MaxInt64
	}
	if v < 0 {
		return -v
	}
	return v
}
