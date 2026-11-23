package recover

import (
	"errors"

	"github.com/pion/rtp"
)

// RTPResult summarizes one deterministic RTP recovery run.
type RTPResult struct {
	Ordered       []*rtp.Packet
	Expected      int // sequence numbers spanning first..last
	Received      int // unique packets present in the input
	Duplicates    int // repeated sequence numbers dropped
	Recovered     int // missing-at-arrival packets that later filled a gap
	Unrecoverable int // gaps that never filled within the window budget
	PastWindow    int // subset of unrecoverable gaps closed out after window
	TailGaps      int // subset still open when the observed stream ended
	Window        int
}

// RecoveryRate returns the share of expected sequence numbers present after
// recovery: (Received) / Expected, clamped to [0,1].
func (r *RTPResult) RecoveryRate() float64 {
	if r.Expected == 0 {
		return 1
	}
	rate := float64(r.Received) / float64(r.Expected)
	if rate > 1 {
		rate = 1
	}
	return rate
}

// ErrWindowTooLarge is returned when the configured window exceeds the cap.
var ErrWindowTooLarge = errors.New("recover: loss window exceeds MaxRecoveryWindow")

// RecoverRTP reorders RTP packets, drops duplicates, and fills sequence gaps
// whenever the missing packet arrives within `window` later packets. A gap
// that remains once window newer sequences have passed is counted as
// unrecoverable. The same input always yields the same ordering and counts.
func RecoverRTP(packets []*rtp.Packet, window int) (*RTPResult, error) {
	if window <= 0 {
		window = 1
	}
	if window > MaxRecoveryWindow {
		return nil, ErrWindowTooLarge
	}
	res := &RTPResult{Window: window}
	if len(packets) == 0 {
		return res, nil
	}

	first := packets[0].SequenceNumber
	present := map[uint16]bool{}
	arrival := []uint16{} // sequence numbers in first-seen arrival order
	var last uint16 = first
	for _, p := range packets {
		if p == nil {
			continue
		}
		if present[p.SequenceNumber] {
			res.Duplicates++
			continue
		}
		present[p.SequenceNumber] = true
		arrival = append(arrival, p.SequenceNumber)
		if seqLess(last, p.SequenceNumber) {
			last = p.SequenceNumber
		}
	}
	res.Expected = int(seqDiff(first, last)) + 1
	res.Received = len(arrival)

	// A sequence number was "recovered" if an earlier-arriving packet had a
	// higher number (it filled a gap observed at arrival time).
	recovered := map[uint16]bool{}
	maxSeen := first
	for _, sn := range arrival {
		if seqLess(sn, maxSeen) {
			recovered[sn] = true
		} else {
			maxSeen = sn
		}
	}

	// Walk the full range. A gap with at least `window` newer sequences
	// beyond it has exhausted its recovery budget (past window); a gap near
	// the end stayed open because the observed stream stopped. Both are
	// unrecoverable, but the distinction is reported for verification.
	for sn := first; seqLessEq(sn, last); sn++ {
		if present[sn] {
			if recovered[sn] {
				res.Recovered++
			}
			continue
		}
		res.Unrecoverable++
		if int(seqDiff(sn, last)) >= window {
			res.PastWindow++
		} else {
			res.TailGaps++
		}
	}

	// Deterministic ordered output of the packets that are present.
	for sn := first; seqLessEq(sn, last); sn++ {
		if present[sn] {
			res.Ordered = append(res.Ordered, findPacket(packets, sn))
		}
	}
	return res, nil
}

func findPacket(packets []*rtp.Packet, sn uint16) *rtp.Packet {
	for _, p := range packets {
		if p != nil && p.SequenceNumber == sn {
			return p
		}
	}
	return nil
}

// seqLess handles uint16 wrap for forward sequence comparison.
func seqLess(a, b uint16) bool { return int16(b-a) > 0 }

func seqLessEq(a, b uint16) bool { return a == b || seqLess(a, b) }

// seqDiff returns the forward distance b-a in sequence space.
func seqDiff(a, b uint16) uint16 { return b - a }
