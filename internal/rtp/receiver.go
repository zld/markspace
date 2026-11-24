// Package rtp receives RTP packets, classifies duplicates/reordering/loss by
// sequence number and timestamp, and recovers recoverable losses inside a
// configured window. All bookkeeping is wraparound-safe and deterministic:
// replaying the same packets always yields identical counters and ordering.
package rtp

import (
	"github.com/pion/rtp"

	"mediad/internal/model"
)

// Classification is what one incoming packet turned out to be.
type Classification string

const (
	ClassInOrder   Classification = "in_order"
	ClassReordered Classification = "reordered"
	ClassDuplicate Classification = "duplicate"
	ClassLate      Classification = "late_unrecoverable"
)

// Outcome is the result of feeding one wire packet into the receiver.
type Outcome struct {
	Classification Classification `json:"classification"`
	// Recovered is positive only when this packet closed a pending gap whose
	// missing sequence number was still inside the recovery window.
	Recovered int `json:"recovered"`
}

// Stats is the deterministic replay summary.
type Stats struct {
	Received      int     `json:"received"`
	Duplicates    int     `json:"duplicates"`
	Reordered     int     `json:"reordered"`
	RecoveredLoss int     `json:"recovered_loss"`
	Unrecoverable int     `json:"unrecoverable"`
	RecoveryRate  float64 `json:"recovery_rate"` // recovered / total gaps
}

// Receiver reorders an RTP stream with a fixed recovery window measured in
// sequence-number distance.
//
// Concurrency: a Receiver is a single-writer state machine. Feed packets from
// one goroutine (the typical receive-loop model); Write/Snapshot/Flush are not
// safe for concurrent invocation on the same instance. Cross-goroutine
// handoff should happen through channels, not shared Receiver access.
type Receiver struct {
	window uint16
	// started distinguishes the first packet from a real backtrack.
	started bool
	// highest is the largest sequence number accepted so far (as uint16).
	highest uint16
	// openGaps holds missing sequence numbers still inside the window.
	openGaps map[uint16]bool
	// closed marks finalized sequence numbers for duplicate detection.
	closed map[uint16]bool

	stats Stats
}

// NewReceiver builds a receiver. window is the maximum sequence-number
// distance inside which a missing packet may still arrive and be recovered.
func NewReceiver(window uint16) *Receiver {
	if window == 0 {
		window = 1
	}
	return &Receiver{
		window:   window,
		openGaps: map[uint16]bool{},
		closed:   map[uint16]bool{},
	}
}

// Parse unmarshals one wire packet through pion/rtp and converts any truncation
// into the kernel's structured RTP_TRUNCATED error.
func Parse(buf []byte) (*rtp.Packet, error) {
	p := &rtp.Packet{}
	if err := p.Unmarshal(buf); err != nil {
		return nil, model.New(model.ErrRTPTruncated, "%v", err).AtOffset(int64(len(buf)))
	}
	return p, nil
}

// Write feeds one parsed packet into the receiver and returns its outcome.
func (r *Receiver) Write(p *rtp.Packet) Outcome {
	r.stats.Received++
	sn := p.SequenceNumber
	if !r.started {
		r.started = true
		r.highest = sn
		r.closed[sn] = true
		return Outcome{Classification: ClassInOrder}
	}

	if r.closed[sn] {
		r.stats.Duplicates++
		return Outcome{Classification: ClassDuplicate}
	}

	dist := seqDistance(sn, r.highest)
	if dist <= 0 {
		// Older than the high-water mark: recoverable only within the window.
		if -dist > int32(r.window) {
			r.closed[sn] = true
			r.stats.Unrecoverable++
			return Outcome{Classification: ClassLate}
		}
		r.stats.Reordered++
		out := Outcome{Classification: ClassReordered}
		if r.openGaps[sn] {
			delete(r.openGaps, sn)
			r.closed[sn] = true
			r.stats.RecoveredLoss++
			out.Recovered = 1
		} else {
			// A not-yet-seen old sequence outside any registered gap (for
			// example the first packet of a restarted source) is still valid;
			// closing it keeps later copies detectable as duplicates.
			r.closed[sn] = true
		}
		return out
	}

	// Forward jump: every sequence number between highest+1 and sn-1 is a gap.
	for missing := r.highest + 1; missing != sn; missing++ {
		if !r.closed[missing] {
			r.openGaps[missing] = true
		}
	}
	r.highest = sn
	r.closed[sn] = true
	// Advancing may push old gaps outside the recovery window.
	r.expireGaps()
	return Outcome{Classification: ClassInOrder}
}

// expireGaps finalizes gaps whose distance from the high-water mark exceeds
// the recovery window, counting them as unrecoverable losses.
func (r *Receiver) expireGaps() {
	for g := range r.openGaps {
		d := seqDistance(g, r.highest) // negative while g is behind highest
		if -d > int32(r.window) {
			delete(r.openGaps, g)
			r.closed[g] = true
			r.stats.Unrecoverable++
		}
	}
}

// Flush ends the stream: any gap still open is now unrecoverable because no
// more packets will arrive. Call once after the final Write.
func (r *Receiver) Flush() Stats {
	for range r.openGaps {
		r.stats.Unrecoverable++
	}
	r.openGaps = map[uint16]bool{}
	return r.Snapshot()
}

// Snapshot returns the cumulative stats with the recovery rate computed once,
// deterministically, in units of sequence-number gaps.
func (r *Receiver) Snapshot() Stats {
	s := r.stats
	totalGaps := s.RecoveredLoss + s.Unrecoverable
	if totalGaps > 0 {
		s.RecoveryRate = float64(s.RecoveredLoss) / float64(totalGaps)
	}
	return s
}

// OpenGapCount exposes how many missing packets are still within the window.
func (r *Receiver) OpenGapCount() int { return len(r.openGaps) }

// seqDistance returns how far ahead a is relative to b in signed sequence
// space, handling 16-bit wraparound. Positive means a is newer than b.
func seqDistance(a, b uint16) int32 {
	return int32(int16(a - b))
}
