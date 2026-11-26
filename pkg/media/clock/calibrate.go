package clock

import (
	"fmt"

	"cmafrepair/pkg/media/timebase"
)

// Boundary is one fragment start (moof boundary) as seen by one rendition.
type Boundary struct {
	Representation string
	Sequence       uint32
	MediaTime      uint64 // tfdt-domain start, at the anchor's ClockHz
	SharedMs       int64  // filled by the calibrator
	Isolated       bool   // filled by the calibrator when divergent
}

// Divergence explains one representation that fell outside the one-tick band.
type Divergence struct {
	Representation string
	Sequence       uint32
	ExpectedMs     int64
	GotMs          int64
	DeltaMs        int64
	Msg            string // non-numeric isolation reason (e.g. missing anchor)
}

// String renders the divergence for terminal reports.
func (d Divergence) String() string {
	if d.Msg != "" {
		return fmt.Sprintf("%s seq=%d isolated: %s", d.Representation, d.Sequence, d.Msg)
	}
	return fmt.Sprintf("%s seq=%d got=%dms want=%dms delta=%dms",
		d.Representation, d.Sequence, d.GotMs, d.ExpectedMs, d.DeltaMs)
}

// Calibrator fans per-representation boundaries into the shared clock and
// checks that every healthy rendition places the same logical boundary within
// one clock tick (1 ms). A divergent rendition is isolated for that boundary;
// the remaining renditions keep processing.
type Calibrator struct {
	anchors map[string]ClockAnchor // keyed by representation name
}

// NewCalibrator builds an empty calibrator.
func NewCalibrator() *Calibrator {
	return &Calibrator{anchors: map[string]ClockAnchor{}}
}

// SetAnchor registers (or replaces) the anchor for one representation.
func (c *Calibrator) SetAnchor(rep string, a ClockAnchor) error {
	if a.ClockHz == 0 {
		return timebase.NewError(timebase.CodeInputInvalid, timebase.PhaseClock,
			"anchor for %s has zero clock rate", rep)
	}
	c.anchors[rep] = a
	return nil
}

// Anchor returns the stored anchor for a representation.
func (c *Calibrator) Anchor(rep string) (ClockAnchor, bool) {
	a, ok := c.anchors[rep]
	return a, ok
}

// Calibrate maps every boundary to shared-clock milliseconds, picks the first
// healthy representation as the reference, and isolates any representation
// whose boundary diverges by more than one tick. Isolation is per boundary so
// one bad measurement never poisons the whole rendition.
func (c *Calibrator) Calibrate(bounds []Boundary) (refMs int64, divs []Divergence, err error) {
	if len(bounds) == 0 {
		return 0, nil, timebase.NewError(timebase.CodeInputInvalid, timebase.PhaseClock,
			"no boundaries to calibrate")
	}
	healthy := make([]Boundary, 0, len(bounds))
	for i := range bounds {
		b := &bounds[i]
		a, ok := c.anchors[b.Representation]
		if !ok {
			b.Isolated = true
			divs = append(divs, Divergence{
				Representation: b.Representation,
				Sequence:       b.Sequence,
				Msg:            "no clock anchor",
			})
			continue
		}
		ms, perr := a.MediaToShared(b.MediaTime)
		if perr != nil {
			b.Isolated = true
			divs = append(divs, Divergence{
				Representation: b.Representation,
				Sequence:       b.Sequence,
				Msg:            perr.Error(),
			})
			continue
		}
		b.SharedMs = ms
		healthy = append(healthy, *b)
	}
	if len(healthy) == 0 {
		return 0, divs, timebase.NewError(timebase.CodeClockDivergence, timebase.PhaseClock,
			"every representation lacked a usable anchor")
	}
	refMs = healthy[0].SharedMs
	for _, b := range healthy[1:] {
		ok, terr := withinOneMs(b.SharedMs, refMs)
		if terr != nil {
			return refMs, divs, terr
		}
		if !ok {
			divs = append(divs, Divergence{
				Representation: b.Representation,
				Sequence:       b.Sequence,
				ExpectedMs:     refMs,
				GotMs:          b.SharedMs,
				DeltaMs:        b.SharedMs - refMs,
			})
			for j := range bounds {
				if bounds[j].Representation == b.Representation {
					bounds[j].Isolated = true
				}
			}
		}
	}
	return refMs, divs, nil
}

// withinOneMs compares two millisecond instants with the rational one-tick
// rule from the timebase package.
func withinOneMs(aMs, bMs int64) (bool, error) {
	a, err := timebase.NewRat(aMs, timebase.SharedClockHz)
	if err != nil {
		return false, err
	}
	b, err := timebase.NewRat(bMs, timebase.SharedClockHz)
	if err != nil {
		return false, err
	}
	return timebase.WithinOneTick(a, b)
}
