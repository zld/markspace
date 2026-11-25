package rtp

import (
	"github.com/pion/rtp"

	"mediasync/internal/model"
)

// RecoveryStats summarises one representation's loss repair outcome.
type RecoveryStats struct {
	Gaps        int // missing sequence numbers detected inside closed windows
	Recovered   int // gaps repaired from redundancy inside the window
	Lost        int // gaps that could not be repaired, kept as located errors
	WindowSkips int // gaps outside the recovery window, never touched
}

// BoundaryChecker validates that a repaired packet would land on a legal
// segment boundary. Recovery is refused when it returns false, matching the
// rule that loss repair only runs when boundary validation passes.
type BoundaryChecker func(seq uint16) bool

// Recoverer repairs gaps inside a closed reorder window using deterministic
// redundancy packets supplied by the fixture. It never synthesises payload:
// a gap without a matching redundancy entry is reported as a located loss.
type Recoverer struct {
	repID      string
	window     int
	redundancy map[uint16][]byte // seq -> repair payload provided by sender
	stats      RecoveryStats
	losses     []*model.MediaError
}

// NewRecoverer creates a recoverer. window is the maximum gap distance from
// the highest observed sequence number that remains eligible for repair.
func NewRecoverer(repID string, window int, redundancy map[uint16][]byte) *Recoverer {
	if window < 1 {
		window = 1
	}
	cp := make(map[uint16][]byte, len(redundancy))
	for k, v := range redundancy {
		cp[k] = append([]byte(nil), v...)
	}
	return &Recoverer{repID: repID, window: window, redundancy: cp}
}

// AnalyzeWindow inspects the ordered pion RTP packets that survived buffering
// and repairs every interior gap. Existing packets and repaired entries are
// merged into one ordered stream; unrepaired gaps appear in Losses() rather
// than as data. The highest sequence number defines the window edge.
func (r *Recoverer) AnalyzeWindow(packets []*rtp.Packet, ok BoundaryChecker) []*rtp.Packet {
	if len(packets) == 0 {
		return nil
	}
	present := make(map[uint16]bool, len(packets))
	var high uint16
	for i, p := range packets {
		present[p.SequenceNumber] = true
		if i == 0 || seqDiff(p.SequenceNumber, high) > 0 {
			high = p.SequenceNumber
		}
	}

	repaired := make(map[uint16]*rtp.Packet)
	// Walk every sequence number from the first packet up to the highest. The
	// uint16 cursor wraps naturally at the 16-bit boundary.
	start := packets[0].SequenceNumber
	for cursor := start; seqDiff(cursor, high) <= 0; cursor++ {
		if present[cursor] {
			continue
		}
		r.stats.Gaps++
		// Rule: recovery only inside the window and only with boundary check.
		if seqDiff(high, cursor) > r.window {
			r.stats.WindowSkips++
			r.losses = append(r.losses, model.NewError(model.KindPacketLost, "rtp",
				int64(cursor), "rep %s seq=%d outside recovery window", r.repID, cursor))
			r.stats.Lost++
			continue
		}
		if ok != nil && !ok(cursor) {
			r.losses = append(r.losses, model.NewError(model.KindMissingKeyframe, "rtp",
				int64(cursor), "rep %s seq=%d fails boundary check, refusing repair",
				r.repID, cursor))
			r.stats.Lost++
			continue
		}
		payload, have := r.redundancy[cursor]
		if !have {
			r.losses = append(r.losses, model.NewError(model.KindPacketLost, "rtp",
				int64(cursor), "rep %s seq=%d unrecoverable: no redundancy, no data synthesized",
				r.repID, cursor))
			r.stats.Lost++
			continue
		}
		// Rebuild a pion packet carrying the sender redundancy; its RTP
		// timestamp is derived from its sequence position so timing stays
		// monotonic across the repair.
		repaired[cursor] = &rtp.Packet{
			Header:  rtp.Header{SequenceNumber: cursor, Timestamp: uint32(cursor) * 90},
			Payload: append([]byte(nil), payload...),
		}
		r.stats.Recovered++
	}

	out := make([]*rtp.Packet, 0, len(packets)+len(repaired))
	out = append(out, packets...)
	for _, pkt := range repaired {
		out = append(out, pkt)
	}
	sortPackets(out)
	return out
}

// Stats returns the accumulated recovery counters.
func (r *Recoverer) Stats() RecoveryStats { return r.stats }

// Losses returns every unrepaired gap as a located error. The positions are
// preserved so downstream components can render the gap in the timeline.
func (r *Recoverer) Losses() []*model.MediaError {
	out := make([]*model.MediaError, len(r.losses))
	copy(out, r.losses)
	return out
}
