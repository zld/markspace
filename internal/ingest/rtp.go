// Package ingest turns wire and file evidence into the pipeline's model.
//
// RTP packets become media samples, MP4 sample tables become samples, and
// timed metadata carriers (ID3, emsg, SEI, subtitles, chapters) become events.
// Timestamps are decoded through the repair subsystem; rawness is preserved.
package ingest

import (
	"fmt"

	"github.com/pion/rtp"

	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/model"
)

// PacketObservation is one RTP packet as received, including enough context
// to explain loss and reordering decisions.
type PacketObservation struct {
	Seq      uint16
	RawTicks int64
	Marker   bool
	Payload  []byte
	SSRC     uint32
}

// RTPOptions configures the RTP reassembler.
type RTPOptions struct {
	Track     model.TrackID
	Timescale uint32
	// Modulus defaults to the 32-bit RTP timestamp modulus.
	Modulus int64
	Jitter  int64
	// FragmentGapTicks is the maximum timestamp spread inside one access
	// unit. A packet further away starts a new access unit.
	FragmentGapTicks int64
	// AnchorFirst makes the first packet's timestamp the track origin.
	AnchorFirst bool
	// AllowDroppedGap controls whether skipped sequence numbers are recorded
	// as recoverable loss (true) or treated as fatal (false).
	AllowDroppedGap bool
}

// RTPResult is the reassembled output of one RTP stream.
type RTPResult struct {
	Samples     []model.Sample
	Events      []model.Event
	Decisions   []clock.Decision
	Errors      []*model.Error
	LostPackets int
	Reordered   int
	// Packets counts observations consumed.
	Packets int
}

// RTPReassembler turns RTP observations into samples keyed by timestamp.
// A change of RTP timestamp (with the packet's marker bit closing the
// previous unit) marks an access-unit boundary.
type RTPReassembler struct {
	opts      RTPOptions
	repairer  *clock.Repairer
	errors    model.ErrorList
	decisions []clock.Decision
	samples   []model.Sample
	res       RTPResult
	started   bool
	lastSeq   uint16
	cur       *accessUnit
	units     []accessUnit
}

type accessUnit struct {
	rawTicks int64
	payload  []byte
	marker   bool
	seq      uint16
}

// NewRTPReassembler validates options.
func NewRTPReassembler(opts RTPOptions) (*RTPReassembler, error) {
	if opts.Track == "" {
		opts.Track = model.TrackVideoMain
	}
	if opts.Timescale == 0 {
		return nil, model.NewError(opts.Track, model.StageParse, model.CodeInvalidParameter, false,
			"RTP timescale must be non-zero")
	}
	if opts.Modulus == 0 {
		opts.Modulus = clock.Default32Modulus
	}
	if opts.FragmentGapTicks == 0 {
		opts.FragmentGapTicks = 1
	}
	return &RTPReassembler{opts: opts}, nil
}

// Push consumes one observation.
func (a *RTPReassembler) Push(obs PacketObservation) error {
	if a.repairer == nil {
		r, err := clock.NewRepairer(a.opts.Track, clock.RepairOptions{
			Timescale:     a.opts.Timescale,
			Modulus:       a.opts.Modulus,
			JitterTicks:   a.opts.Jitter,
			AnchorToFirst: a.opts.AnchorFirst,
		})
		if err != nil {
			return err
		}
		a.repairer = r
	}
	return a.consume(obs)
}

// DecodeRTPPacket is a convenience wrapper that parses wire bytes with
// pion/rtp and pushes the result. Malformed input is classified, not panicked.
func (a *RTPReassembler) DecodeRTPPacket(raw []byte) error {
	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw); err != nil {
		return a.errors.Add(model.WrapError(a.opts.Track, model.StageParse, model.CodeMalformedPacket, true, err,
			"RTP header could not be parsed"))
	}
	return a.Push(PacketObservation{
		Seq:      pkt.SequenceNumber,
		RawTicks: int64(pkt.Timestamp),
		Marker:   pkt.Marker,
		Payload:  pkt.Payload,
		SSRC:     pkt.SSRC,
	})
}

func (a *RTPReassembler) consume(obs PacketObservation) error {
	a.res.Packets++
	if a.started {
		if err := a.accountSequence(obs.Seq); err != nil {
			return err
		}
	} else {
		a.started = true
	}
	a.lastSeq = obs.Seq

	// Timestamp decode is done when the unit is flushed so that reordering is
	// visible to the repairer in observation order.
	if a.cur != nil && !sameUnit(a.cur, obs, a.opts.FragmentGapTicks) {
		a.flush()
	}
	if a.cur == nil {
		a.cur = &accessUnit{rawTicks: obs.RawTicks, seq: obs.Seq}
	}
	a.cur.payload = append(a.cur.payload, obs.Payload...)
	a.cur.marker = obs.Marker
	if obs.Marker {
		a.flush()
	}
	return nil
}

// pendingUnits returns the buffered access units in order without consuming
// them; used by tests and streaming observers.
func (a *RTPReassembler) pendingUnits() []accessUnit {
	out := append([]accessUnit(nil), a.units...)
	if a.cur != nil {
		out = append(out, *a.cur)
	}
	return out
}

func sameUnit(cur *accessUnit, obs PacketObservation, gap int64) bool {
	d := obs.RawTicks - cur.rawTicks
	if d < 0 {
		d = -d
	}
	return d <= gap
}

func (a *RTPReassembler) accountSequence(seq uint16) error {
	delta := int16(seq - a.lastSeq)
	switch {
	case delta == 1:
		return nil
	case delta <= 0:
		a.res.Reordered++
		a.decisions = append(a.decisions, clock.Decision{
			Track: a.opts.Track, Kind: clock.DecisionReorder, Recoverable: true,
			Message: fmt.Sprintf("RTP sequence %d arrived after %d", seq, a.lastSeq),
		})
		return nil
	case delta > 1:
		lost := int(delta) - 1
		a.res.LostPackets += lost
		if !a.opts.AllowDroppedGap && lost > 8 {
			err := a.errors.Add(model.NewError(a.opts.Track, model.StageParse, model.CodeUnrecoverableLoss, false,
				"RTP gap of %d packets between %d and %d", lost, a.lastSeq, seq))
			return err
		}
		a.errors.Add(model.NewError(a.opts.Track, model.StageParse, model.CodeMissingPayload, true,
			"RTP gap of %d packets between %d and %d", lost, a.lastSeq, seq))
	}
	return nil
}

func (a *RTPReassembler) flush() {
	if a.cur == nil {
		return
	}
	u := *a.cur
	a.cur = nil
	if len(u.payload) == 0 {
		a.errors.Add(model.NewError(a.opts.Track, model.StageParse, model.CodeMissingPayload, true,
			"access unit at timestamp %d carried no payload", u.rawTicks))
		return
	}
	a.units = append(a.units, u)
}

func (a *RTPReassembler) recordError(err error) {
	pe, ok := model.AsError(err)
	if ok {
		a.errors.Add(pe)
		return
	}
	a.errors.Add(model.WrapError(a.opts.Track, model.StageParse, model.CodeMalformedPacket, false, err,
		"RTP processing failed"))
}

// Samples returns the samples accumulated so far (used by streaming callers).
func (a *RTPReassembler) Samples() []model.Sample { return a.samples }
