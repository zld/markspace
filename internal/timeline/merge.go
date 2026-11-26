// Package timeline merges subtitle and media events on the shared tick base.
//
// Events advance an event-time watermark. A watermark horizon H = maxSeen -
// lateWindow divides the stream into a sealed prefix (times < H) and an
// unordered tail (times >= H). An event arriving below H is unrecoverable and
// is reported with the original horizon. An event arriving inside the window
// (H <= t < maxSeen) is inserted into the tail and forces the affected
// interval to be recomputed; events ahead of the watermark simply extend the
// tail. The final sequence is the sealed prefix followed by the canonically
// sorted tail, which equals a full event-time sort and is therefore identical
// no matter how often the same arrival sequence is replayed.
package timeline

import (
	"fmt"
	"sort"

	"mediasync/internal/clock"
	"mediasync/internal/issue"
)

// Kind identifies the event source.
type Kind string

const (
	KindCue      Kind = "cue"
	KindKeyframe Kind = "keyframe"
	KindPacket   Kind = "packet"
)

// Event is one timestamped unit on the timeline.
type Event struct {
	Kind   Kind
	Time   clock.Tick
	End    clock.Tick
	Input  string
	Line   int
	Offset int64
	Packet int
	Ref    string
}

// Location converts the event position for diagnostics.
func (e Event) Location() issue.Location {
	return issue.Location{Input: e.Input, Line: e.Line, ByteOffset: e.Offset, Packet: e.Packet, Ref: e.Ref}
}

// Less is the canonical, arrival-independent ordering.
func (e Event) Less(o Event) bool {
	if e.Time != o.Time {
		return e.Time < o.Time
	}
	if e.Kind != o.Kind {
		return e.Kind < o.Kind
	}
	if e.Input != o.Input {
		return e.Input < o.Input
	}
	if e.Line != o.Line {
		return e.Line < o.Line
	}
	if e.Offset != o.Offset {
		return e.Offset < o.Offset
	}
	if e.Packet != o.Packet {
		return e.Packet < o.Packet
	}
	if e.Ref != o.Ref {
		return e.Ref < o.Ref
	}
	return e.End < o.End
}

func (e Event) same(o Event) bool {
	return e.Kind == o.Kind && e.Input == o.Input && e.Line == o.Line &&
		e.Offset == o.Offset && e.Packet == o.Packet && e.Ref == o.Ref &&
		e.Time == o.Time && e.End == o.End
}

// Gap is the derived interval between two adjacent canonical events.
type Gap struct {
	From, To Event
	Delta    clock.Tick
	Overlaps bool
}

// Replay records one rollback recomputation.
type Replay struct {
	Event      Event
	Horizon    clock.Tick // horizon at admission (the original watermark)
	PrevMax    clock.Tick
	From, To   int // tail indices recomputed, inclusive From, exclusive To
	Recomputed int
}

// Timeline is the streaming merge engine. It is not safe for concurrent use.
type Timeline struct {
	lateWindow clock.Tick
	maxSeen    clock.Tick
	started    bool
	sealed     []Event
	tail       []Event
	issues     issue.List
	replays    []Replay
	admitted   int
	recomputed int
	unrecov    int
	sealedN    int
}

// New creates a timeline with the given late window.
func New(lateWindow clock.Tick) *Timeline {
	if lateWindow < 0 {
		lateWindow = 0
	}
	return &Timeline{lateWindow: lateWindow}
}

// LateWindow returns the configured window.
func (t *Timeline) LateWindow() clock.Tick { return t.lateWindow }

// Horizon is the oldest event time that can still be inserted.
func (t *Timeline) Horizon() clock.Tick {
	if !t.started {
		return 0
	}
	return t.maxSeen - t.lateWindow
}

// MaxSeen is the greatest event time admitted.
func (t *Timeline) MaxSeen() clock.Tick { return t.maxSeen }

// Push admits one event.
func (t *Timeline) Push(ev Event) {
	if ev.Offset == 0 {
		ev.Offset = -1
	}
	if !t.started {
		t.started = true
		t.maxSeen = ev.Time
		t.tail = append(t.tail, ev)
		t.admitted++
		return
	}
	horizon := t.Horizon()
	if ev.Time < horizon {
		t.unrecov++
		it := issue.New(issue.LateUnrecoverable, ev.Input,
			fmt.Sprintf("%s %s at %s is older than watermark horizon %s (watermark %s, late window %s)",
				ev.Kind, ev.Ref, ev.Time, horizon, t.maxSeen, t.lateWindow)).
			WithLine(ev.Line).WithByte(ev.Offset).WithPacket(ev.Packet).WithRef(ev.Ref)
		t.issues.Add(it)
		return
	}
	prevMax := t.maxSeen

	insertAt := len(t.tail)
	for i := range t.tail {
		if ev.Less(t.tail[i]) {
			insertAt = i
			break
		}
	}
	t.tail = append(t.tail, Event{})
	copy(t.tail[insertAt+1:], t.tail[insertAt:])
	t.tail[insertAt] = ev
	t.admitted++

	if ev.Time > t.maxSeen {
		t.maxSeen = ev.Time
	}
	if ev.Time < prevMax {
		// Late but inside the window: every adjacency from the insertion
		// point to the previous frontier is recomputed.
		t.recomputed += len(t.tail) - insertAt - 1
		t.replays = append(t.replays, Replay{
			Event: ev, Horizon: horizon, PrevMax: prevMax,
			From: insertAt, To: len(t.tail), Recomputed: len(t.tail) - insertAt - 1,
		})
	}
	t.seal()
}

// seal moves tail events that fell below the horizon into the sealed prefix.
func (t *Timeline) seal() {
	if len(t.tail) == 0 {
		return
	}
	horizon := t.Horizon()
	sort.SliceStable(t.tail, func(i, j int) bool { return t.tail[i].Less(t.tail[j]) })
	cut := 0
	for cut < len(t.tail) && t.tail[cut].Time < horizon {
		cut++
	}
	if cut == 0 {
		return
	}
	t.sealed = append(t.sealed, t.tail[:cut]...)
	t.sealedN += cut
	t.tail = append([]Event(nil), t.tail[cut:]...)
}

// Finalize seals the remainder logically and is idempotent.
func (t *Timeline) Finalize() {}

// Events returns the canonical event sequence: sealed prefix plus sorted tail.
func (t *Timeline) Events() []Event {
	t.seal()
	out := make([]Event, 0, len(t.sealed)+len(t.tail))
	out = append(out, t.sealed...)
	out = append(out, t.tail...)
	return out
}

// Gaps returns adjacency intervals in canonical order.
func (t *Timeline) Gaps() []Gap {
	ev := t.Events()
	gaps := make([]Gap, 0, len(ev))
	for i := 0; i+1 < len(ev); i++ {
		a, b := ev[i], ev[i+1]
		g := Gap{From: a, To: b, Delta: b.Time - a.Time}
		if a.End > a.Time && a.End > b.Time {
			g.Overlaps = true
		}
		gaps = append(gaps, g)
	}
	return gaps
}

// Issues returns watermark diagnostics in deterministic order.
func (t *Timeline) Issues() issue.List { return t.issues.Sorted() }

// Replays returns rollback records in arrival order.
func (t *Timeline) Replays() []Replay { return t.replays }

// Counters exposes admission statistics for reports.
func (t *Timeline) Counters() (admitted, recomputed, unrecoverable, sealed int) {
	return t.admitted, t.recomputed, t.unrecov, t.sealedN
}

// DescribeReplay formats a replay record without wall-clock data.
func DescribeReplay(r Replay) string {
	return fmt.Sprintf("late %s %s at %s (watermark %s, horizon %s) replayed tail [%d,%d) = %d intervals",
		r.Event.Kind, r.Event.Ref, r.Event.Time, r.PrevMax, r.Horizon, r.From, r.To, r.Recomputed)
}
