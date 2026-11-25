package clock

import (
	"fmt"

	"mediasync/internal/issue"
)

// Watermark drives streaming timeline assembly. Events at or beyond
// horizon-lateWindow are immediately committed; events inside the window may
// still arrive out of order and trigger a rollback/replay of the affected
// interval; anything older than the window is unrecoverable.
//
// The watermark is advanced by event time, not by arrival order, so repeated
// runs over the same input sequence produce the same result.
type Watermark struct {
	LateWindow Tick

	horizon   Tick // oldest event time still replayable
	maxSeen   Tick // maximum event time admitted so far
	started   bool
	committed int
	replayed  int
}

// NewWatermark creates a guard with the given late window.
func NewWatermark(lateWindow Tick) *Watermark {
	if lateWindow < 0 {
		lateWindow = 0
	}
	return &Watermark{LateWindow: lateWindow}
}

// Horizon is the oldest event time that may still be inserted.
func (w *Watermark) Horizon() Tick { return w.horizon }

// MaxSeen is the greatest event time observed.
func (w *Watermark) MaxSeen() Tick { return w.maxSeen }

// Replayed counts events that required a rollback recomputation.
func (w *Watermark) Replayed() int { return w.replayed }

// Committed counts events advanced past the window without replay.
func (w *Watermark) Committed() int { return w.committed }

// Result classifies one event admission.
type Result int

const (
	// Admitted means the event is within the replayable window.
	Admitted Result = iota
	// LateReplay means a committed-range event arrived and must be replayed.
	LateReplay
	// LateUnrecoverable means the event is older than the horizon and cannot
	// be folded into the timeline without contradicting the watermark.
	LateUnrecoverable
)

func (r Result) String() string {
	switch r {
	case Admitted:
		return "admitted"
	case LateReplay:
		return "late_replay"
	case LateUnrecoverable:
		return "late_unrecoverable"
	}
	return "unknown"
}

// Observe admits an event at time t with a location for diagnostics.
func (w *Watermark) Observe(t Tick, loc issue.Location) (Result, *issue.Issue) {
	if !w.started {
		w.started = true
		w.maxSeen = t
		w.horizon = t - w.LateWindow
		w.committed++
		return Admitted, nil
	}
	res := Admitted
	switch {
	case t < w.horizon:
		res = LateUnrecoverable
	case t < w.maxSeen:
		res = LateReplay
	}
	if t > w.maxSeen {
		w.maxSeen = t
		w.horizon = t - w.LateWindow
	}
	switch res {
	case LateReplay:
		w.replayed++
	case Admitted:
		w.committed++
	}
	if res == LateUnrecoverable {
		it := issue.New(issue.LateUnrecoverable, loc.Input,
			fmt.Sprintf("event time %s is older than watermark horizon %s (late window %s)",
				t, w.horizon, w.LateWindow)).WithLine(loc.Line).WithByte(loc.ByteOffset).WithPacket(loc.Packet).WithRef(loc.Ref)
		return res, &it
	}
	return res, nil
}

// Clone returns an independent copy, used by the timeline engine when it
// attempts a speculative replay.
func (w *Watermark) Clone() *Watermark {
	c := *w
	return &c
}
