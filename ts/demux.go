package ts

import (
	"errors"
	"io"

	"mediad/internal/clock"
)

// TrackEvent is the single event type handed to every analysis subsystem.
// All time fields are monotonic 90 kHz ticks on one PCR-expanded timeline,
// so silence, slicing and fingerprints can never disagree about time.
type TrackEvent struct {
	TrackID string
	PID     uint16
	Type    uint8
	// Ordinal is the packet's index in the full input (0-based).
	Ordinal int64
	Offset  int64
	Start   int64 // monotonic tick at which this payload begins
	End     int64 // monotonic tick at which the next packet begins
	Payload []byte
}

// DemuxResult collects everything the TS subsystem establishes.
type DemuxResult struct {
	Timeline   *clock.Timeline
	Map        *ProgramMap
	Events     []TrackEvent
	Errors     []clock.RangeError
	Duplicates int
	Packets    int64
	// InputBytes is the total number of bytes consumed, including any
	// truncated tail (its offset is reported in Errors).
	InputBytes int64
}

// Demux runs the full TS correction pipeline over r: 188-byte scan, PSI
// version/CRC gating, continuity validation and PCR timeline expansion.
// Duplicate packets are dropped before any event is emitted. The returned
// error is non-nil only for fatal input corruption (sync loss that reaches
// EOF, truncation or oversize); structured anomalies live in result.Errors.
func Demux(r io.Reader, opts ScanOptions) (*DemuxResult, error) {
	tl := clock.NewTimeline()
	psi := NewPSITracker()
	cc := NewContinuityTracker()
	res := &DemuxResult{Timeline: tl}

	// lastTick tracks interpolation state per PID: most recent mono tick and
	// the packet ordinal at which it was observed.
	type interp struct {
		tick    int64
		ordinal int64
	}
	lastTick := map[uint16]interp{}
	pending := map[uint16][]TrackEvent{} // events waiting for the next PCR anchor

	var fatal error
	var ordinal int64
	scanErr := Scan(r, opts, func(e Event) error {
		if e.SyncError != nil {
			res.Errors = append(res.Errors, *e.SyncError)
			return nil
		}
		p := e.Pkt
		res.Packets++
		res.InputBytes = p.Offset + PacketSize
		ordinal = res.Packets - 1

		// PSI tables update the effective map only when CRC-valid and newer.
		if _, err := psi.Feed(p); err != nil {
			return err
		}
		// Continuity: duplicates are ignored, jumps become structured errors.
		if cc.Feed(p, ordinal) {
			return nil
		}
		// PCR expands wrap/discontinuity onto the monotonic timeline.
		anchor := int64(-1)
		if raw, ok := p.PCR(); ok {
			anchor = tl.Feed(raw)
			lastTick[p.PID] = interp{tick: anchor, ordinal: ordinal}
			// Flush events that were waiting for this PID's first anchor.
			if queued, ok := pending[p.PID]; ok {
				for i := range queued {
					queued[i].Start = anchor
					queued[i].End = anchor
				}
				res.Events = append(res.Events, queued...)
				delete(pending, p.PID)
			}
		}
		if !p.HasPayload() || p.PID == PIDPAT || psi.isKnownPMT(p.PID) {
			return nil
		}
		ev := TrackEvent{
			TrackID: trackID(psi, p.PID), PID: p.PID, Ordinal: ordinal,
			Offset: p.Offset, Payload: append([]byte{}, p.Payload...),
		}
		if st, ok := lastTick[p.PID]; ok {
			// Extend the previous event to this packet's interpolated tick.
			ev.Start = st.tick + (ordinal-st.ordinal)*ticksPerPacket
			ev.End = ev.Start + ticksPerPacket
			if anchor >= 0 {
				ev.Start, ev.End = anchor, anchor+ticksPerPacket
			}
			res.Events = append(res.Events, ev)
		} else {
			// No anchor yet; hold the event until the first PCR for the PID.
			pending[p.PID] = append(pending[p.PID], ev)
		}
		return nil
	})
	if scanErr != nil {
		fatal = scanErr
		if re, ok := asRange(scanErr); ok {
			res.Errors = append(res.Errors, re)
			res.InputBytes = re.End
		}
	}
	res.Map = psi.Current()
	res.Errors = append(res.Errors, psi.Errs...)
	for _, ev := range cc.Events {
		res.Errors = append(res.Errors, ev.Report())
	}
	res.Errors = append(res.Errors, tl.Errs...)
	res.Duplicates = cc.Duplicates()
	return res, fatal
}

// ticksPerPacket is the constant per-packet duration assumed for elementary
// streams before a PCR anchor refines it (90 kHz ticks per 188-byte packet at
// the generator's fixed cadence).
const ticksPerPacket = 90

func trackID(psi *PSITracker, pid uint16) string {
	for _, ss := range psi.Current().Streams {
		for _, s := range ss {
			if s.PID == pid {
				return s.TrackID
			}
		}
	}
	return "pid-" + hex4(pid)
}

func hex4(v uint16) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[v>>12], digits[v>>8&0xf], digits[v>>4&0xf], digits[v&0xf]})
}

func asRange(err error) (clock.RangeError, bool) {
	var re clock.RangeError
	if errors.As(err, &re) {
		return re, true
	}
	return clock.RangeError{}, false
}
