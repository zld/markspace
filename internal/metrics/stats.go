// Package metrics accounts for every packet and classifies continuity
// counter behaviour per PID.
package metrics

type PIDStats struct {
	PID               uint16
	Packets           int64
	Duplicates        int64
	Jumps             int64
	TransportErrors   int64
	AFOnly            int64
	UnknownGaps       int64
	Scrambled         int64
	DiscontinuitySeen bool
	// LastCC is the continuity counter of the final packet seen.
	LastCC uint8
	HasCC  bool
	// broken is set when a transport error or explicit discontinuity means
	// the next counter value cannot be checked against the previous one.
	broken bool
}

// Tracker classifies continuity for every PID it sees.
type Tracker struct {
	pids  map[uint16]*PIDStats
	Total int64
}

// NewTracker creates an empty tracker.
func NewTracker() *Tracker { return &Tracker{pids: make(map[uint16]*PIDStats)} }

// Stats returns the mutable statistics for pid, creating them if needed.
func (t *Tracker) Stats(pid uint16) *PIDStats {
	s, ok := t.pids[pid]
	if !ok {
		s = &PIDStats{PID: pid}
		t.pids[pid] = s
	}
	return s
}
