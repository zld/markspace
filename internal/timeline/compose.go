package timeline

import (
	"sort"

	"avcheck/internal/media"
)

// SourceState records where one output frame's per-track content came from.
type SourceState struct {
	TrackID uint32
	Frame   media.FrameState // clean, recovered or placeholder
	Track   TrackState
}

// OutputFrame is one slot of the composed timeline.
type OutputFrame struct {
	Index   uint64
	Time    media.Rat // shared time base, monotonic non-decreasing
	Sources []SourceState
	Primary *media.Frame // lowest-track-id real frame, nil if all placeholder
}

// Composer aligns tracks by absolute frame sequence number.
//
// Rules (deterministic):
//   - output index range is [min track start, max track end];
//   - a track with no frame at an index contributes a placeholder;
//   - output time is the max of present track times, clamped to be
//     monotonic non-decreasing against the previous output;
//   - sources are ordered by track id.
type Composer struct {
	clock  media.Clock
	tracks []*Track
}

// NewComposer sorts tracks by id for deterministic output.
func NewComposer(clock media.Clock, tracks ...*Track) *Composer {
	ts := append([]*Track(nil), tracks...)
	sort.Slice(ts, func(i, j int) bool { return ts[i].ID < ts[j].ID })
	return &Composer{clock: clock, tracks: ts}
}

// Compose builds the full output timeline and commits every index.
func (c *Composer) Compose() ([]OutputFrame, error) {
	lo, hi, ok := c.span()
	if !ok {
		return nil, nil
	}
	out := make([]OutputFrame, 0, hi-lo+1)
	var prev media.Rat
	havePrev := false
	for seq := lo; seq <= hi; seq++ {
		of := OutputFrame{Index: seq}
		var best media.Rat
		have := false
		for _, t := range c.tracks {
			ss := SourceState{TrackID: t.ID, Track: t.State}
			if f, ok := t.Get(seq); ok {
				ss.Frame = f.State
				if of.Primary == nil {
					of.Primary = f
				}
				ft, err := c.clock.ToSeconds(f.Ticks)
				if err != nil {
					return nil, err
				}
				if !have || ft.Cmp(best) > 0 {
					best = ft
				}
				have = true
			} else {
				ss.Frame = media.StatePlaceholder
			}
			of.Sources = append(of.Sources, ss)
		}
		switch {
		case !have && havePrev:
			of.Time = prev // total gap: hold previous time
		case !have:
			of.Time = media.Rat{Num: 0, Den: 1}
		case havePrev && best.Cmp(prev) < 0:
			of.Time = prev // clamp: never go backwards
		default:
			of.Time = best
		}
		prev = of.Time
		havePrev = true
		out = append(out, of)
	}
	for _, t := range c.tracks {
		for seq := lo; seq <= hi; seq++ {
			t.MarkCommitted(seq)
		}
	}
	return out, nil
}

func (c *Composer) span() (lo, hi uint64, ok bool) {
	for _, t := range c.tracks {
		l, h, o := t.Range()
		if !o {
			continue
		}
		if !ok || l < lo {
			lo = l
		}
		if !ok || h > hi {
			hi = h
		}
		ok = true
	}
	return lo, hi, ok
}

// Tracks returns the composed tracks in deterministic order.
func (c *Composer) Tracks() []*Track { return c.tracks }
