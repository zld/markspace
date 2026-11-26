// Package ts demuxes MPEG-TS packet streams recovered from RTP payloads.
// Parsing is strictly packet-aligned at 188 bytes: every sync, truncation and
// continuity-counter fault carries the packet offset and, where known, its
// time range. The isolate policy skips affected intervals and keeps parsing;
// the strict policy returns the first fault as an error.
package ts

import (
	"context"
	"io"

	"rtpregress/internal/clock"
)

const (
	// PacketSize is the fixed MPEG-TS packet length in bytes.
	PacketSize = 188
	syncByte   = 0x47

	hdrSize       = 4
	afLenIndex    = 4
	pcrLen        = 6
	pesHeaderSize = 3
)

// Policy selects fault handling.
type Policy uint8

const (
	// PolicyIsolate skips faulty intervals and continues parsing.
	PolicyIsolate Policy = iota
	// PolicyStrict returns an error on the first input fault.
	PolicyStrict
)

// ParsePolicy converts a CLI string ("isolate"/"strict").
func ParsePolicy(s string) (Policy, bool) {
	switch s {
	case "", "isolate":
		return PolicyIsolate, true
	case "strict":
		return PolicyStrict, true
	default:
		return 0, false
	}
}

// PESPacket is one assembled PES on an audio PID with its integer timeline.
type PESPacket struct {
	PID        uint16
	Offset     int64  // offset of the first TS packet of the PES
	PTSNanos   int64  // presentation timestamp on the global nanosecond line
	DTSNanos   int64  // decode timestamp when present, else equals PTS
	Data       []byte // elementary stream bytes
	StreamType uint8
}

// Gap marks an interval where no trustworthy media exists. No samples are
// synthesized; the audio stage bridges these with silence at the timeline.
type Gap struct {
	PID       uint16
	Offset    int64
	FromNanos int64
	ToNanos   int64
	Code      clock.Code
	Detail    string
}

// TimelineEvent is emitted in input order. Exactly one pointer is non-nil.
type TimelineEvent struct {
	PES *PESPacket
	PSI *PSIEvent
	Gap *Gap
}

// Stats summarizes a demux run.
type Stats struct {
	Packets       int64
	PCRSeen       int64
	PESPackets    int64
	PSIEvents     int64
	SyncErrors    int64
	Truncations   int64
	CCJumps       int64
	Gaps          int64
	DriftPPM      int64
	PCRWraps      int64
	MaxErrorNanos int64
}

type pidState struct {
	lastCC     uint8
	hasCC      bool
	pesBuf     []byte
	pesStart   int64
	pesPTS     int64
	pesDTS     int64
	pesHasTS   bool
	streamType uint8
	lastPTSNS  int64
}

// Demuxer reads raw 188-byte TS packets and emits a monotonic timeline.
type Demuxer struct {
	r         io.Reader
	policy    Policy
	psi       *PSITracker
	pcrUnwrap *clock.PCRUnwrapper
	rtpUnwrap *clock.RTPUnwrapper
	drift     *clock.DriftEstimator
	baseNS    int64 // nanos of PCR tick zero on the global line
	baseSet   bool
	pids      map[uint16]*pidState
	audioPIDs map[uint16]uint8 // PID -> stream type
	pmtPIDs   map[uint16]bool
	stats     Stats
	buf       [PacketSize]byte
	offset    int64

	lastRTPNanos int64
	haveRTP      bool
	ptsUnwrap    pts33Unwrapper
}

// NewDemuxer configures a demuxer over an aligned TS byte stream.
func NewDemuxer(r io.Reader, policy Policy) *Demuxer {
	return &Demuxer{
		r:         r,
		policy:    policy,
		psi:       NewPSITracker(),
		pcrUnwrap: &clock.PCRUnwrapper{},
		rtpUnwrap: &clock.RTPUnwrapper{},
		drift:     &clock.DriftEstimator{},
		pids:      map[uint16]*pidState{},
		audioPIDs: map[uint16]uint8{},
		pmtPIDs:   map[uint16]bool{},
	}
}

// fail applies the policy to a parser fault. Isolate-mode faults are counted
// as gaps by the caller when it builds a Gap event.
func (d *Demuxer) fail(err error) error {
	e := asClock(err)
	if d.policy == PolicyStrict {
		return e
	}
	d.stats.Gaps++
	return nil
}

// Run drains the whole stream, invoking onEvent in strict order. It returns
// io.ErrUnexpectedEOF-derived faults according to policy; a clean end is nil.
func (d *Demuxer) Run(ctx context.Context, onEvent func(TimelineEvent)) (Stats, error) {
	for {
		if err := ctx.Err(); err != nil {
			return d.stats, clock.New(clock.CodeCanceled, "ts", d.offset, 0, 0,
				"demux canceled: %v", err)
		}
		n, err := io.ReadFull(d.r, d.buf[:])
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			if n > 0 {
				terr := clock.New(clock.CodeTSTruncated, "ts", d.offset, 0, 0,
					"trailing %d bytes, need %d", n, PacketSize)
				d.stats.Truncations++
				if perr := d.fail(terr); perr != nil {
					return d.stats, perr
				}
			}
			d.flushAll(onEvent)
			d.stats.DriftPPM = clock.RoundPPM(d.drift.PPM())
			d.stats.PCRWraps = d.pcrUnwrap.Wraps()
			return d.stats, nil
		}
		if err != nil {
			return d.stats, err
		}
		if ferr := d.processPacket(d.buf[:], onEvent); ferr != nil {
			return d.stats, ferr
		}
		d.offset += PacketSize
		d.stats.Packets++
	}
}

func (d *Demuxer) processPacket(pkt []byte, onEvent func(TimelineEvent)) error {
	off := d.offset
	if pkt[0] != syncByte {
		e := clock.New(clock.CodeTSSync, "ts", off, 0, 0,
			"expected sync byte 0x47, got 0x%02x", pkt[0])
		d.stats.SyncErrors++
		if err := d.fail(e); err != nil {
			return err
		}
		// isolate: drop the whole 188-byte interval and keep parsing
		onEvent(TimelineEvent{Gap: &Gap{PID: 0, Offset: off, Code: clock.CodeTSSync, Detail: e.Message}})
		return nil
	}
	h := parseHeader(pkt)
	st := d.pids[h.PID]
	if st == nil {
		st = &pidState{}
		d.pids[h.PID] = st
	}
	if err := d.checkContinuity(h, st, off); err != nil {
		// A CC jump on a continuation packet corrupts the in-flight PES. A
		// PUSI packet starts a fresh PES anyway, so only discard buffered
		// bytes when this is not itself a new PES start.
		if !h.PUSI {
			st.pesBuf = st.pesBuf[:0]
			st.pesHasTS = false
		}
		if ferr := d.fail(err); ferr != nil {
			return ferr
		}
		onEvent(TimelineEvent{Gap: &Gap{
			PID: h.PID, Offset: off, Code: clock.CodeTSCCJump, Detail: err.Error(),
		}})
	}
	if h.HasAdaptation {
		if err := d.processAdaptation(pkt, h, off); err != nil {
			if ferr := d.fail(err); ferr != nil {
				return ferr
			}
		}
	}
	if !h.HasPayload {
		return nil
	}
	payload, perr := payloadBytes(pkt, h, off)
	if perr != nil {
		if ferr := d.fail(perr); ferr != nil {
			return ferr
		}
		return nil
	}
	switch {
	case h.PID == pidPAT:
		evs, err := d.psi.Push(h.PID, off, h.PUSI, payload)
		if err != nil {
			if ferr := d.fail(asClock(err)); ferr != nil {
				return ferr
			}
			return nil
		}
		for _, ev := range evs {
			d.applyPSI(ev, onEvent, off)
		}
	case d.pmtPIDs[h.PID]:
		evs, err := d.psi.Push(h.PID, off, h.PUSI, payload)
		if err != nil {
			if ferr := d.fail(asClock(err)); ferr != nil {
				return ferr
			}
			return nil
		}
		for _, ev := range evs {
			d.applyPSI(ev, onEvent, off)
		}
	default:
		if stType, ok := d.audioPIDs[h.PID]; ok {
			st.streamType = stType
			d.processAudioPES(h, st, payload, off, onEvent)
		}
	}
	return nil
}

func (d *Demuxer) applyPSI(ev *PSIEvent, onEvent func(TimelineEvent), off int64) {
	if ev == nil {
		return
	}
	d.stats.PSIEvents++
	if ev.Kind == "PAT" {
		for _, p := range ev.AudioPIDs {
			d.pmtPIDs[p] = true
		}
	}
	if ev.Kind == "PMT" {
		for pid, st := range ev.AudioTypes {
			if _, known := d.audioPIDs[pid]; !known || d.audioPIDs[pid] != st {
				// audio PID or codec change: open a new segment, keep global time
				d.audioPIDs[pid] = st
				if ps := d.pids[pid]; ps != nil {
					ps.pesBuf = ps.pesBuf[:0]
				}
			}
		}
	}
	ev.Offset = off
	onEvent(TimelineEvent{PSI: ev})
}

func asClock(err error) *clock.Error {
	if e := clock.AsError(err); e != nil {
		return e
	}
	return clock.New(clock.CodeTSPSI, "ts", -1, 0, 0, "%v", err)
}
