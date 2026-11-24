package timeline

import (
	"sort"

	"mediarc/internal/model"
)

// Watermark tracks the largest event time observed and the commit horizon.
// The horizon is maxPTS - allowedLateness. It is monotonic: late input never
// moves a watermark backwards.
type Watermark struct {
	maxPTS          *model.Timestamp
	horizon         *model.Timestamp
	allowedLateness model.Timestamp
}

func NewWatermark(allowedLatenessMS int64) *Watermark {
	d := model.Milliseconds(allowedLatenessMS)
	return &Watermark{allowedLateness: d}
}

// Observe records an event timestamp and reports whether the maximum changed.
func (w *Watermark) Observe(ts model.Timestamp) bool {
	if err := ts.Base.Validate(); err != nil {
		// Timestamps are constructed internally with valid bases; keep the
		// watermark total rather than introducing an error into ordering.
		return false
	}
	advanced := w.maxPTS == nil || ts.After(*w.maxPTS)
	if advanced {
		cp := ts
		w.maxPTS = &cp
		h := ts.Add(model.Timestamp{Ticks: -w.allowedLateness.Ticks, Base: w.allowedLateness.Base})
		w.horizon = &h
	}
	return advanced
}

func (w *Watermark) MaxPTS() (model.Timestamp, bool) {
	if w.maxPTS == nil {
		return model.Timestamp{}, false
	}
	return *w.maxPTS, true
}

func (w *Watermark) Horizon() (model.Timestamp, bool) {
	if w.horizon == nil {
		return model.Timestamp{}, false
	}
	return *w.horizon, true
}

// LateGapMS returns maxPTS - ts in milliseconds. Negative means future event.
func (w *Watermark) LateGapMS(ts model.Timestamp) int64 {
	if w.maxPTS == nil {
		return 0
	}
	maxMS := w.maxPTS.Millis()
	return maxMS - ts.Millis()
}

// IsBeyondLateness reports whether an event is older than the permitted
// replay boundary. Boundary equality is accepted.
func (w *Watermark) IsBeyondLateness(ts model.Timestamp, hardBoundaryMS int64) bool {
	return w.LateGapMS(ts) > hardBoundaryMS
}

// SortEvents deterministically orders events by rational PTS, track, ID and
// arrival sequence.
func SortEvents(events []Event) []Event {
	out := append([]Event(nil), events...)
	sort.SliceStable(out, func(i, j int) bool {
		if c := out[i].PTS.Compare(out[j].PTS); c != 0 {
			return c < 0
		}
		if out[i].TrackID != out[j].TrackID {
			return out[i].TrackID < out[j].TrackID
		}
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].ArrivalSeq < out[j].ArrivalSeq
	})
	return out
}
