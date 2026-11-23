package timeline

import (
	"sort"

	"mediad/internal/model"
)

// Replay maintains the emitted output stream and recomputes it when late
// events arrive. All mutations are deterministic: identical input
// sequences always yield identical output order and counters.
type Replay struct {
	outputs []*model.Event // sorted by (EventMs, Seq, Kind)
	// LateReplayed counts events inserted after their emit deadline but
	// inside the late window. LateDropped counts those beyond it.
	LateReplayed int
	LateDropped  int
}

// NewReplay creates an empty replay log.
func NewReplay() *Replay { return &Replay{} }

// Append emits an on-time event. Callers must append in (EventMs, Seq)
// order, which the jitter buffer already guarantees.
func (r *Replay) Append(ev *model.Event) {
	r.outputs = append(r.outputs, ev)
}

// InsertSorted places an event recovered before its loss was finalized.
// It does not count as a replay: the loss was never declared.
func (r *Replay) InsertSorted(ev *model.Event) {
	i := sort.Search(len(r.outputs), func(i int) bool {
		return less(ev, r.outputs[i])
	})
	r.outputs = append(r.outputs, nil)
	copy(r.outputs[i+1:], r.outputs[i:])
	r.outputs[i] = ev
}

// InsertLate inserts an event that arrived after its emit deadline.
// Inside the late window the event is replayed into the output stream
// and counted; beyond the window it is dropped and counted. Returns
// whether the event was inserted.
func (r *Replay) InsertLate(ev *model.Event, wm *Watermark) bool {
	if !wm.InLateWindow(ev.EventMs) {
		r.LateDropped++
		return false
	}
	r.InsertSorted(ev)
	r.LateReplayed++
	return true
}

// Outputs returns the current output stream in emission order.
func (r *Replay) Outputs() []*model.Event { return r.outputs }

func less(a, b *model.Event) bool {
	if a.EventMs != b.EventMs {
		return a.EventMs < b.EventMs
	}
	if a.Seq != b.Seq {
		return a.Seq < b.Seq
	}
	return a.Kind < b.Kind
}
