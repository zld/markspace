package timeline

import "medcheck/internal/model"

// SourceSwitch reports the boundary key change between consecutive frames. It
// returns true when track or source changed, meaning a new boundary begins and
// wrap/error state must not be carried across.
func SourceSwitch(prev, cur *model.Frame) bool {
	if prev == nil {
		return cur != nil
	}
	return prev.Track != cur.Track || prev.Source != cur.Source
}

// BoundaryCount counts distinct (track, source) boundaries in a view.
func (v *View) BoundaryCount() int { return len(v.Boundaries) }

// Replay runs the same frames through a fresh engine in the given arrival order
// and returns the resulting view. Because the view is a pure function of the
// frame set, any two arrival orders must yield identical views.
func Replay(frames []*model.Frame, lateWindowMicros int64) *View {
	e := NewEngine(lateWindowMicros)
	for _, f := range frames {
		e.Push(f)
	}
	return e.View()
}

// BoundaryFor returns the view boundary matching track/source, or nil.
func (v *View) BoundaryFor(track, source uint32) *BoundaryView {
	for _, b := range v.Boundaries {
		if b.Track == track && b.Source == source {
			return b
		}
	}
	return nil
}
