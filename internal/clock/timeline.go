package clock

import (
	"fmt"
	"sort"
)

// ByteRange identifies a span of input bytes affected by an event.
type ByteRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// Event is the single error/observation model every module records against.
// All timestamps are expressed on the shared 90 kHz time base.
type Event struct {
	Kind     string    `json:"kind"`             // sync_error|cc_gap|cc_dup|truncated|ambiguous_ts|psi_update|loss|late|rtp_dup|unsupported
	Severity string    `json:"severity"`         // info|warning|error
	PID      uint16    `json:"pid,omitempty"`    // TS PID the event belongs to, when known
	Track    string    `json:"track,omitempty"`  // audio|video|unknown
	Range    ByteRange `json:"range"`            // affected input bytes
	At90k    int64     `json:"at_90k,omitempty"` // timeline position, -1 when unknown
	Detail   string    `json:"detail,omitempty"` // human-readable explanation
}

// Recorder accumulates events in deterministic order.
type Recorder struct {
	events []Event
}

// Add appends an event.
func (r *Recorder) Add(e Event) {
	if e.At90k == 0 {
		e.At90k = -1
	}
	r.events = append(r.events, e)
}

// Events returns a copy of the recorded events.
func (r *Recorder) Events() []Event {
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return out
}

// CountByKind returns how many events of each kind were recorded.
func (r *Recorder) CountByKind() map[string]int {
	m := map[string]int{}
	for _, e := range r.events {
		m[e.Kind]++
	}
	return m
}

// Clock90k is the 90 kHz tick frequency shared by PCR/PTS.
const Clock90k uint64 = 90000

// Timeline keeps one unwrapper per PID and maps packets onto a shared
// millisecond time base anchored at the first valid PCR.
type Timeline struct {
	pcr     *Unwrapper33
	pts     map[uint16]*Unwrapper33
	origin  int64 // first unwrapped PCR in 90 kHz units
	hasOrig bool
}

// NewTimeline creates an empty timeline.
func NewTimeline() *Timeline {
	return &Timeline{pcr: NewUnwrapper33(), pts: map[uint16]*Unwrapper33{}}
}

// FeedPCR unwraps a raw PCR_base (33-bit, 90 kHz). The boolean result reports
// whether the value was ambiguous; ambiguous PCRs are rejected, so a PCR
// wraparound can never move the timeline backwards.
func (t *Timeline) FeedPCR(raw uint64) (ms int64, at90k int64, ambiguous bool) {
	res := t.pcr.Feed(raw)
	if res.Ambiguous {
		return 0, -1, true
	}
	if !t.hasOrig {
		t.origin, t.hasOrig = int64(res.Value), true
	}
	at90k = int64(res.Value) - t.origin
	return at90k * 1000 / int64(Clock90k), at90k, false
}

// FeedPTS unwraps a raw PTS (33-bit, 90 kHz) for a given PID and returns its
// position relative to the PCR origin.
func (t *Timeline) FeedPTS(pid uint16, raw uint64) (ms int64, at90k int64, ambiguous bool) {
	u, ok := t.pts[pid]
	if !ok {
		u = NewUnwrapper33()
		t.pts[pid] = u
	}
	res := u.Feed(raw)
	if res.Ambiguous {
		return 0, -1, true
	}
	if !t.hasOrig {
		return 0, int64(res.Value), false
	}
	at90k = int64(res.Value) - t.origin
	return at90k * 1000 / int64(Clock90k), at90k, false
}

// PIDs returns the sorted list of PIDs with PTS state.
func (t *Timeline) PIDs() []uint16 {
	out := make([]uint16, 0, len(t.pts))
	for pid := range t.pts {
		out = append(out, pid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Describe renders an event as a stable, one-line diagnostic string.
func (e Event) Describe() string {
	return fmt.Sprintf("%s pid=%d track=%s bytes=[%d,%d) %s",
		e.Kind, e.PID, e.Track, e.Range.Start, e.Range.End, e.Detail)
}
