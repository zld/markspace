// Package continuity tracks the 4-bit transport-stream continuity counter
// independently for every PID. After a gap the tracker resynchronises on the
// actually received counter, so later packets are never misreported as lost.
package continuity

import (
	"sort"
)

// Stats is the per-PID continuity accounting.
type Stats struct {
	PID          uint16 `json:"pid"`
	ValidPackets int    `json:"valid_packets"`     // payload-bearing, in-order packets
	Duplicates   int    `json:"duplicate_packets"` // same counter repeated
	GapEvents    int    `json:"gap_events"`        // number of discontinuities
	GapPackets   int    `json:"continuity_gaps"`   // total missing packets across gaps
	ErrorPackets int    `json:"error_packets"`     // transport_error_indicator set
	FirstCounter int    `json:"first_counter"`     // -1 until first usable packet
	LastCounter  int    `json:"last_counter"`      // counter of the most recent valid packet
}

type pidState struct {
	stats     Stats
	hasPrev   bool
	prev      uint8
	// prevDup tracks the counter of an accepted duplicate so repeated
	// duplicates are counted individually.
}

// Tracker holds continuity state for all PIDs seen in the stream.
type Tracker struct {
	pids map[uint16]*pidState
}

// New creates an empty tracker.
func New() *Tracker {
	return &Tracker{pids: map[uint16]*pidState{}}
}

// Observe records one packet. Error packets are counted but never participate
// in continuity reasoning (their payload is unusable). hasPayload mirrors the
// transport header payload flag; adaptation-only packets carry no counter
// obligation beyond a legal duplicate.
func (t *Tracker) Observe(pid uint16, cc uint8, hasPayload, transportError bool) {
	st := t.pids[pid]
	if st == nil {
		st = &pidState{stats: Stats{PID: pid, FirstCounter: -1, LastCounter: -1}}
		t.pids[pid] = st
	}

	if transportError {
		st.stats.ErrorPackets++
		return
	}

	// Adaptation-only packets (no payload) do not advance the stream content;
	// per ISO/IEC 13818-1 the counter stays equal to the previous packet.
	if !hasPayload {
		if st.hasPrev && cc == st.prev {
			st.stats.Duplicates++
		}
		return
	}

	if !st.hasPrev {
		st.hasPrev = true
		st.prev = cc
		st.stats.FirstCounter = int(cc)
		st.stats.LastCounter = int(cc)
		st.stats.ValidPackets++
		return
	}

	if cc == st.prev {
		// Same counter with payload: a repeated packet, not a gap.
		st.stats.Duplicates++
		return
	}

	if cc != st.prev {
		// Forward distance on the 4-bit counter ring. A legal +1 step (which
		// includes 15 -> 0 wrap) yields distance 1 and hence zero missing
		// packets; any larger forward jump means the intervening counters were
		// never received.
		distance := (int(cc) - int(st.prev)) & 0x0f
		if distance > 1 {
			missing := distance - 1
			st.stats.GapEvents++
			st.stats.GapPackets += missing
		}
	}
	// Resynchronise on the counter actually received; subsequent packets are
	// judged relative to this value, so one gap does not poison the rest.
	st.prev = cc
	st.stats.LastCounter = int(cc)
	st.stats.ValidPackets++
}

// Stats returns per-PID statistics in ascending PID order.
func (t *Tracker) Stats() []Stats {
	pids := make([]uint16, 0, len(t.pids))
	for pid := range t.pids {
		pids = append(pids, pid)
	}
	sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })

	out := make([]Stats, 0, len(pids))
	for _, pid := range pids {
		out = append(out, t.pids[pid].stats)
	}
	return out
}

// TotalGapPackets sums missing-packet counts across every PID.
func (t *Tracker) TotalGapPackets() int {
	total := 0
	for _, st := range t.pids {
		total += st.stats.GapPackets
	}
	return total
}
