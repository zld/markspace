package timeline

import (
	"sort"

	"medcheck/internal/model"
)

// BoundaryView is one (track, source) segment inside a committed snapshot.
// Errors from one boundary are never merged into another.
type BoundaryView struct {
	Track       uint32
	Source      uint32
	Frames      []*model.Frame
	MinOffset   model.RawOffset
	MaxOffset   model.RawOffset
	StartMicros int64
	EndMicros   int64
	Errors      []*model.CodeError
}

// View is an immutable, deterministically derived snapshot. It is a pure
// function of the retained in-window frame set, so arrival order cannot change
// it. Out-of-window frames never enter a View and therefore never alter it.
type View struct {
	Ordered         []*model.Frame
	Boundaries      []*BoundaryView
	WatermarkMicros int64
	MaxEventMicros  int64
	FrameCount      int
}

// Engine accepts frames in arrival order, maintains the event-time water mark
// and atomically replaces the derived View whenever in-window late data
// requires recomputation.
type Engine struct {
	lateWindowMicros int64
	retained         []*model.Frame
	maxEventMicros   int64
	hasMax           bool
	view             *View
	recomputeCount   int
	lastFrom         model.RawOffset
	outOfWindow      []*model.Frame
}

// NewEngine builds an Engine. lateWindowMicros is the allowed lateness; a frame
// whose event time is older than (maxEvent - window) is out of window.
func NewEngine(lateWindowMicros int64) *Engine {
	if lateWindowMicros < 0 {
		lateWindowMicros = 0
	}
	return &Engine{lateWindowMicros: lateWindowMicros}
}

// Push ingests one frame. reorderSeq is the position at which it arrived, used
// only for diagnostics; deterministic ordering never depends on it.
func (e *Engine) Push(f *model.Frame) (recomputed bool, affected model.RawOffset) {
	if f == nil {
		return false, model.RawOffset{}
	}
	eventMicros := f.EventTime.MicrosOr(f.EventTime.Ticks)
	watermark := int64(0)
	if e.hasMax {
		watermark = e.maxEventMicros - e.lateWindowMicros
		if watermark < 0 {
			watermark = 0
		}
	}
	inWindow := !e.hasMax || eventMicros >= watermark
	if !inWindow {
		// Beyond the allowed lateness: it cannot move the water mark or alter
		// any committed boundary, so it is held separately and never retained.
		if f.Err == nil {
			f.Err = &model.CodeError{
				Kind:     model.KindTimeBase,
				Track:    f.Track,
				Source:   f.Source,
				Seq:      f.Seq,
				Offset:   f.Offset,
				FirstPos: f.Offset.Start,
				Message:  "frame arrived beyond late window",
			}
		}
		e.outOfWindow = append(e.outOfWindow, f)
		return false, model.RawOffset{}
	}

	// A new maximum always extends the horizon and therefore the window.
	if !e.hasMax || eventMicros > e.maxEventMicros {
		e.maxEventMicros = eventMicros
		e.hasMax = true
	}

	e.retained = append(e.retained, f)

	affected = f.Offset
	e.lastFrom = affected
	e.recomputeCount++
	e.view = e.buildView()
	return true, affected
}

// frameOrdering is the total deterministic order key.
func frameOrdering(a, b *model.Frame) bool {
	if a.Track != b.Track {
		return a.Track < b.Track
	}
	if a.Source != b.Source {
		return a.Source < b.Source
	}
	am, bm := a.EventTime.MicrosOr(a.EventTime.Ticks), b.EventTime.MicrosOr(b.EventTime.Ticks)
	if am != bm {
		return am < bm
	}
	if a.Seq != b.Seq {
		return a.Seq < b.Seq
	}
	return a.Offset.Start < b.Offset.Start
}

// buildView derives a fresh snapshot purely from the retained frames.
func (e *Engine) buildView() *View {
	ordered := make([]*model.Frame, len(e.retained))
	copy(ordered, e.retained)
	sort.SliceStable(ordered, func(i, j int) bool { return frameOrdering(ordered[i], ordered[j]) })

	watermark := int64(0)
	if e.hasMax {
		watermark = e.maxEventMicros - e.lateWindowMicros
		if watermark < 0 {
			watermark = 0
		}
	}
	v := &View{
		Ordered:         ordered,
		WatermarkMicros: watermark,
		MaxEventMicros:  e.maxEventMicros,
		FrameCount:      len(ordered),
	}

	index := map[[2]uint32]*BoundaryView{}
	var order [][2]uint32
	for _, f := range ordered {
		key := [2]uint32{f.Track, f.Source}
		b, ok := index[key]
		if !ok {
			b = &BoundaryView{Track: f.Track, Source: f.Source, MinOffset: f.Offset, MaxOffset: f.Offset}
			b.StartMicros = f.EventTime.MicrosOr(f.EventTime.Ticks)
			index[key] = b
			order = append(order, key)
		}
		b.Frames = append(b.Frames, f)
		em := f.EventTime.MicrosOr(f.EventTime.Ticks)
		if em < b.StartMicros {
			b.StartMicros = em
		}
		if em > b.EndMicros {
			b.EndMicros = em
		}
		if f.Offset.Start < b.MinOffset.Start {
			b.MinOffset = f.Offset
		}
		if f.Offset.End > b.MaxOffset.End {
			b.MaxOffset = f.Offset
		}
		if f.Err != nil {
			b.Errors = append(b.Errors, f.Err)
		}
	}
	for _, key := range order {
		v.Boundaries = append(v.Boundaries, index[key])
	}
	return v
}

// View returns the committed snapshot (nil before the first in-window frame).
func (e *Engine) View() *View { return e.view }

// OutOfWindow returns frames that arrived beyond the allowed lateness. They are
// kept for reporting but never influence the water mark or the derived view.
func (e *Engine) OutOfWindow() []*model.Frame { return e.outOfWindow }

// RecomputeCount reports how many deterministic recomputations have run.
func (e *Engine) RecomputeCount() int { return e.recomputeCount }

// LastRecomputeFrom is the input offset that triggered the latest recompute.
func (e *Engine) LastRecomputeFrom() model.RawOffset { return e.lastFrom }
