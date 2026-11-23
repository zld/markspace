// Package recovery models RTP backhaul recovery on top of pion/rtp:
// reordering, sequence-number wraparound, duplicate/retransmission
// handling and bounded loss windows with deterministic dispositions.
package recovery

import (
	"fmt"

	"github.com/pion/rtp"

	"mediasync/internal/clock"
	"mediasync/internal/mederr"
)

// Disposition is the deterministic handling decision for one packet.
type Disposition string

const (
	DispAccepted     Disposition = "accepted"      // in-order, first sighting
	DispReordered    Disposition = "reordered"     // late but fills a hole
	DispDuplicate    Disposition = "duplicate"     // same seq, identical bytes
	DispRetransmit   Disposition = "retransmit"    // same seq, new bytes, fills hole
	DispRTXConflict  Disposition = "rtx_conflict"  // same seq, new bytes, already filled
)

// Event is the recorded, deterministic outcome of processing one packet.
type Event struct {
	Seq          uint16
	UnwrappedSeq int64
	Timestamp    uint32
	Disposition  Disposition
	Recoverable  bool
}

// LossWindow describes one contiguous run of missing sequence numbers.
type LossWindow struct {
	FirstSeq    int64
	LastSeq     int64
	Count       int
	Recoverable bool
}

// Report summarises a recovery session.
type Report struct {
	Events        []Event
	Losses        []LossWindow
	Received      int
	UniqueSeqs    int
	Recovered     int
	Unrecoverable int
	Wraps         int
}

// RecoveryRate is Recovered / (Recovered + Unrecoverable), i.e. the share
// of missing packets that were filled before their window aged out.
func (r *Report) RecoveryRate() float64 {
	denom := r.Recovered + r.Unrecoverable
	if denom == 0 {
		return 1
	}
	return float64(r.Recovered) / float64(denom)
}

// Receiver is a per-SSRC reorder/loss tracker.
type Receiver struct {
	maxRTX uint16 // max reorder distance; packets beyond it finalise losses
	unwrap *clock.Unwrapper

	haveHighest bool
	highest     int64 // highest unwrapped seq ever accepted
	buffer      map[int64]*rtp.Packet
	filled      map[int64]bool
	pending     map[int64]bool // holes observed, awaiting fill or ageing
	declared    map[int64]bool // holes already reported as unrecoverable
	rawHash     map[int64]string
	report      Report
}

// NewReceiver builds a Receiver. maxRTX must be within [1, 32767].
func NewReceiver(maxRTX uint16) (*Receiver, error) {
	if maxRTX == 0 || maxRTX > 32767 {
		return nil, mederr.New(mederr.CodeInternal,
			fmt.Sprintf("maxRTX %d out of range", maxRTX))
	}
	u, err := clock.NewUnwrapper(16)
	if err != nil {
		return nil, err
	}
	return &Receiver{
		maxRTX:   maxRTX,
		unwrap:   u,
		buffer:   map[int64]*rtp.Packet{},
		filled:   map[int64]bool{},
		pending:  map[int64]bool{},
		declared: map[int64]bool{},
		rawHash:  map[int64]string{},
	}, nil
}

// Handle processes one raw RTP packet using pion/rtp unmarshalling.
func (rcv *Receiver) Handle(raw []byte) error {
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(raw); err != nil {
		return mederr.New(mederr.CodeInvalidManifest,
			"invalid RTP packet: "+err.Error())
	}
	unwrapped, wrapped, err := rcv.unwrap.Expand(uint64(pkt.SequenceNumber))
	if err != nil {
		return err
	}
	if wrapped {
		rcv.report.Wraps++
	}
	rcv.report.Received++
	fp := fingerprint(pkt)
	ev := Event{Seq: pkt.SequenceNumber, UnwrappedSeq: unwrapped,
		Timestamp: pkt.Timestamp}

	switch {
	case !rcv.haveHighest:
		ev.Disposition, ev.Recoverable = DispAccepted, true
		rcv.haveHighest = true
		rcv.highest = unwrapped
	case rcv.filled[unwrapped]:
		// The slot already has final data: only an identical re-send is
		// harmless; different bytes are an explicit conflict.
		if rcv.rawHash[unwrapped] == fp {
			ev.Disposition = DispDuplicate
		} else {
			ev.Disposition = DispRTXConflict
		}
	case unwrapped > rcv.highest:
		// Register every sequence skipped by this jump as a pending hole.
		for hole := rcv.highest + 1; hole < unwrapped; hole++ {
			if !rcv.filled[hole] && !rcv.declared[hole] {
				rcv.pending[hole] = true
			}
		}
		ev.Disposition, ev.Recoverable = DispAccepted, true
		rcv.highest = unwrapped
	default:
		// A late packet fills a hole inside the reorder window.
		// Reordering = the original transmission arriving late; an
		// explicit retransmission carries new (replacement) bytes.
		if rcv.pending[unwrapped] {
			ev.Disposition = DispRetransmit
			ev.Recoverable = true
			rcv.report.Recovered++
			delete(rcv.pending, unwrapped)
		} else {
			ev.Disposition = DispReordered
		}
	}
	rcv.buffer[unwrapped] = pkt
	rcv.filled[unwrapped] = true
	rcv.rawHash[unwrapped] = fp
	rcv.report.Events = append(rcv.report.Events, ev)
	rcv.finalizeBehind(unwrapped)
	return nil
}

// finalizeBehind scans for holes older than the reorder window and closes
// contiguous runs as single unrecoverable loss windows.
func (rcv *Receiver) finalizeBehind(justSeen int64) {
	edge := rcv.highest - int64(rcv.maxRTX)
	// Only scan from the first observed sequence upward.
	first := rcv.highest
	for s := range rcv.filled {
		if s < first {
			first = s
		}
	}
	var runStart, runEnd int64 = 0, -1
	flush := func() {
		if runEnd < runStart {
			return
		}
		// Merge with the previous window when declarations were split
		// across multiple Handle calls. The previous flush already
		// accounted for its packets in Unrecoverable; only extend size.
		if n := len(rcv.report.Losses); n > 0 {
			last := &rcv.report.Losses[n-1]
			if last.LastSeq+1 == runStart {
				added := int(runEnd-runStart) + 1
				last.LastSeq = runEnd
				last.Count += added
				rcv.report.Unrecoverable += added
				runStart, runEnd = 0, -1
				return
			}
		}
		n := int(runEnd-runStart) + 1
		rcv.report.Losses = append(rcv.report.Losses, LossWindow{
			FirstSeq: runStart, LastSeq: runEnd, Count: n,
			Recoverable: false,
		})
		rcv.report.Unrecoverable += n
		runStart, runEnd = 0, -1
	}
	for s := first; s <= edge; s++ {
		if rcv.filled[s] {
			flush()
			continue
		}
		if rcv.declared[s] {
			continue // already part of a flushed loss window
		}
		rcv.declared[s] = true
		delete(rcv.pending, s)
		if runEnd >= runStart && runEnd+1 == s {
			runEnd = s // extend the current contiguous run
		} else {
			flush()
			runStart, runEnd = s, s
		}
	}
	flush()
}

// Finalize closes the session and returns the immutable report.
func (rcv *Receiver) Finalize() Report {
	rep := rcv.report
	rep.UniqueSeqs = len(rcv.filled)
	rep.Events = append([]Event(nil), rep.Events...)
	rep.Losses = append([]LossWindow(nil), rep.Losses...)
	return rep
}

func fingerprint(p *rtp.Packet) string {
	return fmt.Sprintf("%d:%x", p.Timestamp, p.Payload)
}
