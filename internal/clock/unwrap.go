// Package clock: unwrapping of PCR, PTS and DTS onto one monotonic timeline.
package clock

import (
	"fmt"
)

// Kind identifies a timestamp field within a transport stream.
type Kind uint8

const (
	// PCR is the program clock reference, carried at 27 MHz.
	PCR Kind = iota
	// PTS is the presentation timestamp at 90 kHz.
	PTS
	// DTS is the decode timestamp at 90 kHz.
	DTS
)

// String implements fmt.Stringer.
func (k Kind) String() string {
	switch k {
	case PCR:
		return "PCR"
	case PTS:
		return "PTS"
	case DTS:
		return "DTS"
	default:
		return fmt.Sprintf("Kind(%d)", uint8(k))
	}
}

// Lane groups elementary streams for relative-order checks.
type Lane uint8

const (
	LaneUnknown Lane = iota
	LaneVideo
	LaneAudio
)

// LaneOf maps a stream type name to a lane. Unknown stream types are data.
func LaneOf(streamType string) Lane {
	switch streamType {
	case "video":
		return LaneVideo
	case "audio":
		return LaneAudio
	default:
		return LaneUnknown
	}
}

// Obs is one timestamp observation feeding the unwrap stage.
type Obs struct {
	PID           uint16
	Kind          Kind
	Raw           uint64
	Offset        int64
	Packet        int64
	Lane          Lane
	Discontinuity bool // adaptation-field discontinuity_indicator
}

// basePeriod is the size of the 33-bit PCR *base* field. PCR is unwrapped in
// this 90 kHz domain rather than at 27 MHz: 300*(1<<33) is not a power of
// two, so modular arithmetic is simplest in the base domain where the period
// is exactly 1<<33. The 9-bit extension is retained for spacing but not for
// the shared timeline.
const basePeriod int64 = 1 << 33
const ptsPeriod int64 = 1 << 33 // 90 kHz PTS/DTS field

// spanLimit is how far the timeline may drift from the current anchor before
// the anchor is moved forward. It is a quarter of the 33-bit field, roughly
// 6.6 hours, and only guards against extremely long inputs.
const spanLimit int64 = ptsPeriod / 4

// Unwrapper converts raw timestamp fields into ticks on one shared timeline.
// It is deterministic: the same observation sequence yields the same output.
type Unwrapper struct {
	cfg Config

	// Anchor for the 90 kHz timeline, expressed in the raw PTS/DTS domain
	// so audio and video streams align exactly modulo the 33-bit field.
	anchored  bool
	originRaw uint64
	originTks int64

	lastTks map[uint16]int64
	seenPTS map[uint16]bool

	pcrSeen   map[uint16]bool
	lastPCR   map[uint16]int64 // unwrapped 27 MHz base
	lastPCRTk map[uint16]int64

	firstTks map[Lane]int64
	firstPID map[Lane]uint16
	streams  map[uint16]Lane

	errors ErrorList
}

// NewUnwrapper returns an unwrapper using cfg (zero limits are defaulted).
func NewUnwrapper(cfg Config) *Unwrapper {
	return &Unwrapper{
		cfg:       cfg.Normalize(),
		lastTks:   make(map[uint16]int64),
		seenPTS:   make(map[uint16]bool),
		pcrSeen:   make(map[uint16]bool),
		lastPCR:   make(map[uint16]int64),
		lastPCRTk: make(map[uint16]int64),
		firstTks:  make(map[Lane]int64),
		firstPID:  make(map[Lane]uint16),
		streams:   make(map[uint16]Lane),
	}
}

// SetLane declares the lane of a PID before timestamps arrive.
func (u *Unwrapper) SetLane(pid uint16, lane Lane) {
	u.streams[pid] = lane
}

func wrapDelta(raw, ref int64, period int64) int64 {
	raw &= period - 1
	ref &= period - 1
	d := raw - ref
	if d >= period/2 {
		d -= period
	} else if d < -period/2 {
		d += period
	}
	return d
}

func (u *Unwrapper) addError(kind ErrorKind, obs Obs, raw uint64, blocking bool, detail string) {
	if obs.Lane != LaneUnknown {
		u.streams[obs.PID] = obs.Lane
	}
	u.errors = append(u.errors, &Error{
		Kind: kind, PID: obs.PID, Offset: obs.Offset, PacketIndex: obs.Packet,
		Raw: raw, Detail: detail, Blocking: blocking,
	})
}

// Observe unwraps one timestamp and returns its ticks on the shared timeline.
// ok is false when the value is ambiguous or violates plausibility; in that
// case a blocking diagnostic is recorded and no value is fabricated.
func (u *Unwrapper) Observe(obs Obs) (ticks int64, ok bool) {
	if obs.Kind == PCR {
		return u.observePCR(obs)
	}
	return u.observeTimestamp(obs)
}

func (u *Unwrapper) observePCR(obs Obs) (int64, bool) {
	// obs.Raw is a 27 MHz value; fold it into the 90 kHz base domain first so
	// all wrap arithmetic shares one power-of-two period.
	raw27 := obs.Raw
	base := int64(raw27 / uint64(PCRToTicks))
	base &= basePeriod - 1
	if !u.pcrSeen[obs.PID] {
		u.pcrSeen[obs.PID] = true
		u.lastPCR[obs.PID] = base
		u.lastPCRTk[obs.PID] = base
		// The first PCR also anchors the shared 90 kHz timeline so PTS and
		// PCR can never disagree about which side of a wrap they are on.
		if !u.anchored {
			u.anchored = true
			u.originRaw = uint64(base)
			u.originTks = 0
		}
		return base, true
	}
	d := wrapDelta(base, u.lastPCR[obs.PID], basePeriod)
	if !obs.Discontinuity && d < -u.cfg.MaxReorder {
		u.addError(KindNonMonotonic, obs, raw27, true,
			fmt.Sprintf("PCR moved backward by %d ticks without discontinuity flag", -d))
		return 0, false
	}
	if !obs.Discontinuity && d > u.cfg.MaxJump {
		u.addError(KindDiscontinuity, obs, raw27, true,
			fmt.Sprintf("PCR jumped forward by %d ticks, limit %d", d, u.cfg.MaxJump))
		return 0, false
	}
	u.lastPCR[obs.PID] += d
	u.lastPCRTk[obs.PID] = u.lastPCR[obs.PID]
	return u.lastPCRTk[obs.PID], true
}

func (u *Unwrapper) observeTimestamp(obs Obs) (int64, bool) {
	raw := obs.Raw & (uint64(ptsPeriod) - 1)
	if !u.anchored {
		u.anchored = true
		u.originRaw = raw
		u.originTks = 0
	}
	// Move the anchor when the timeline drifts a quarter-field away, keeping
	// modular decoding unambiguous for very long inputs. A discontinuity
	// flag forces the anchor on so pre/post-reset values stay comparable.
	if u.seenPTS[obs.PID] {
		d := wrapDelta(int64(raw), int64(u.originRaw), ptsPeriod)
		if d > spanLimit || d < -spanLimit || obs.Discontinuity {
			u.originTks += d
			u.originRaw = raw
		}
	}
	d := wrapDelta(int64(raw), int64(u.originRaw), ptsPeriod)
	tks := u.originTks + d

	if u.seenPTS[obs.PID] {
		prev := u.lastTks[obs.PID]
		delta := tks - prev
		switch {
		case delta < -u.cfg.MaxReorder && !obs.Discontinuity:
			u.addError(KindReorder, obs, raw, true,
				fmt.Sprintf("%s moved backward by %d ticks, reorder window %d", obs.Kind, -delta, u.cfg.MaxReorder))
			return 0, false
		case delta > u.cfg.MaxJump && !obs.Discontinuity:
			u.addError(KindDiscontinuity, obs, raw, true,
				fmt.Sprintf("%s jumped forward by %d ticks, limit %d", obs.Kind, delta, u.cfg.MaxJump))
			return 0, false
		}
	}
	u.seenPTS[obs.PID] = true
	u.lastTks[obs.PID] = tks
	if obs.Lane != LaneUnknown {
		u.streams[obs.PID] = obs.Lane
		if _, ok := u.firstTks[obs.Lane]; !ok {
			u.firstTks[obs.Lane] = tks
			u.firstPID[obs.Lane] = obs.PID
		}
	}
	return tks, true
}

// TicksFor returns the last unwrapped ticks of a PID.
func (u *Unwrapper) TicksFor(pid uint16) (int64, bool) {
	v, ok := u.lastTks[pid]
	return v, ok
}

// PCRTicksFor returns the last unwrapped 90 kHz PCR ticks of a PID.
func (u *Unwrapper) PCRTicksFor(pid uint16) (int64, bool) {
	v, ok := u.lastPCRTk[pid]
	return v, ok
}

// FirstTicks returns the first timestamp of a lane.
func (u *Unwrapper) FirstTicks(lane Lane) (int64, bool) {
	v, ok := u.firstTks[lane]
	return v, ok
}

// Errors returns the accumulated diagnostics.
func (u *Unwrapper) Errors() ErrorList {
	return u.errors
}

// Finish validates relative audio/video placement and returns all diagnostics.
// A relative-order violation is blocking: the samples cannot be placed safely.
func (u *Unwrapper) Finish() ErrorList {
	v, vok := u.firstTks[LaneVideo]
	a, aok := u.firstTks[LaneAudio]
	if vok && aok {
		if d := a - v; d > u.cfg.MaxAVDelta || d < -u.cfg.MaxAVDelta {
			pid := u.firstPID[LaneAudio]
			u.errors = append(u.errors, &Error{
				Kind: KindAVOrder, PID: pid, Offset: -1, Raw: 0, Blocking: true,
				Detail: fmt.Sprintf("audio/video first-timestamp delta %d ticks exceeds %d", d, u.cfg.MaxAVDelta),
			})
		}
	}
	u.errors.Sort()
	return u.errors
}
