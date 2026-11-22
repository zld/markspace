package clock

import (
	"fmt"
	"sort"

	"example.com/mediatimeline/internal/model"
)

// Default32Modulus is the modulus of an RTP 32-bit timestamp.
const Default32Modulus int64 = 1 << 32

// DefaultJitterTicks is the default amount by which a timestamp may step
// backwards and still be considered a reordered duplicate instead of a clock
// fault. It is one 90 kHz frame at 30 fps.
const DefaultJitterTicks int64 = 3000

// RepairOptions configures a Repairer.
type RepairOptions struct {
	Timescale uint32
	// Modulus is the timestamp modulus. Use Default32Modulus for RTP and 0 for
	// signed 64-bit MP4 timestamps, which never wrap in practice.
	Modulus int64
	// JitterTicks bounds a tolerated backward step.
	JitterTicks int64
	// AnchorTicks is subtracted from every extended timestamp so that the
	// track starts at zero on the shared program timeline. Set it after the
	// fan-in stage decides the common origin.
	AnchorTicks int64
	// AnchorToFirst treats the first extended timestamp of the stream as time
	// zero. This is what a delivery stream needs: its first packet defines the
	// track origin, even when the raw clock starts near the wrap point.
	AnchorToFirst bool
}

// Repairer turns a stream of raw track timestamps into monotonically
// non-decreasing repaired timestamps on the shared program clock. It records
// every intervention in Decisions.
type Repairer struct {
	track       model.TrackID
	timescale   uint32
	modulus     int64
	jitter      int64
	anchor      int64
	hasLast     bool
	lastRaw     int64
	lastExt     int64
	epoch       int64
	decisions   []Decision
	firstExt    int64
	seen        int64
	wrapSeen    map[int64]bool
	anchorFirst bool
}

// NewRepairer validates options and returns a repairer for track.
func NewRepairer(track model.TrackID, opts RepairOptions) (*Repairer, error) {
	if opts.Timescale == 0 {
		return nil, model.NewError(track, model.StageClock, model.CodeInvalidParameter, false,
			"time unit must have a non-zero timescale")
	}
	if opts.Modulus < 0 {
		return nil, model.NewError(track, model.StageClock, model.CodeInvalidParameter, false,
			"timestamp modulus must not be negative")
	}
	if opts.JitterTicks < 0 {
		return nil, model.NewError(track, model.StageClock, model.CodeInvalidParameter, false,
			"jitter must not be negative")
	}
	jitter := opts.JitterTicks
	if jitter == 0 {
		jitter = DefaultJitterTicks
	}
	return &Repairer{
		track:       track,
		timescale:   opts.Timescale,
		modulus:     opts.Modulus,
		jitter:      jitter,
		anchor:      opts.AnchorTicks,
		anchorFirst: opts.AnchorToFirst,
		wrapSeen:    make(map[int64]bool),
	}, nil
}

// Track returns the repairer's track identifier.
func (r *Repairer) Track() model.TrackID { return r.track }

// Decisions returns the recorded decisions in insertion order.
func (r *Repairer) Decisions() []Decision {
	out := make([]Decision, len(r.decisions))
	copy(out, r.decisions)
	return out
}

// FirstExtended returns the first extended (pre-anchor) tick value, which the
// fan-in stage uses to choose a common origin.
func (r *Repairer) FirstExtended() (int64, bool) { return r.firstExt, r.hasLast }

// SetAnchor re-anchors the repairer. It must be called before decoding samples
// so that all repaired times are relative to the common origin.
func (r *Repairer) SetAnchor(ticks int64) {
	r.anchor = ticks
	r.decisions = append(r.decisions, Decision{
		Track: r.track, Kind: DecisionOrigin, Repaired: ticks, Recoverable: true,
		Message: fmt.Sprintf("anchor set to %d ticks at %d Hz", ticks, r.timescale),
	})
}

// Anchor returns the current anchor in ticks.
func (r *Repairer) Anchor() int64 { return r.anchor }

// Decode converts one raw timestamp into a raw and a repaired timestamp.
func (r *Repairer) Decode(raw int64) (model.TimeStamp, error) {
	ext, state, err := r.extend(raw)
	if err != nil {
		return model.TimeStamp{
			RawTicks: raw,
			RawTime:  r.rawTime(raw),
			State:    model.DecodeRejected,
			Epoch:    r.epoch,
		}, err
	}
	ts := model.TimeStamp{
		RawTicks:     raw,
		RawTime:      r.rawTime(raw),
		RepairedTime: ticksToTime(ext-r.anchor, r.timescale),
		State:        state,
		Epoch:        r.epoch,
	}
	return ts, nil
}

// extend unwraps raw into a signed 64-bit tick position using epoch carry.
// The unwrapped value is the raw tick plus epoch*modulus; it is not clamped,
// so a reordered packet keeps its true position on the timeline.
func (r *Repairer) extend(raw int64) (int64, model.DecodeState, error) {
	if r.modulus > 0 && (raw < 0 || raw >= r.modulus) {
		return 0, model.DecodeRejected, model.NewError(r.track, model.StageClock, model.CodeClockOverflow, false,
			"timestamp %d outside the %d-tick clock", raw, r.modulus)
	}
	if !r.hasLast {
		r.hasLast = true
		r.lastRaw = raw
		r.lastExt = raw
		r.firstExt = raw
		r.seen = 1
		if r.anchorFirst && r.anchor == 0 {
			r.anchor = raw
		}
		r.decisions = append(r.decisions, Decision{
			Track: r.track, Kind: DecisionFirstSeen, RawTicks: raw, Repaired: raw, Recoverable: true,
			Message: "first timestamp establishes the track epoch",
		})
		return raw, model.DecodeExact, nil
	}

	half := int64(0)
	if r.modulus > 0 {
		half = r.modulus / 2
	}
	// First guess the epoch from forward continuity: a large backward step is a
	// wrap into the next epoch, and a large forward step is a packet that
	// belongs to the previous epoch. The guess is then validated on the
	// unwrapped timeline.
	epoch := r.epoch
	fromPrevEpoch := false
	switch {
	case r.modulus > 0 && r.lastRaw-raw > half:
		epoch = r.epoch + 1
	case r.modulus > 0 && raw-r.lastRaw > half:
		// A forward jump larger than half the modulus can only be a packet
		// from the epoch before the last wrap: it is late, not a clock fault.
		epoch = r.epoch - 1
		fromPrevEpoch = true
	}

	ext := raw + epoch*r.modulus
	delta := ext - r.lastExt

	switch {
	case r.modulus > 0 && r.lastRaw-raw > half && epoch == r.epoch+1:
		if _, ok := r.wrapSeen[epoch]; !ok {
			r.wrapSeen[epoch] = true
			r.decisions = append(r.decisions, Decision{
				Track: r.track, Kind: DecisionWrap, RawTicks: raw, Epoch: epoch, Recoverable: true,
				Message: fmt.Sprintf("32-bit timestamp wrap into epoch %d", epoch),
			})
		}
	}

	state := model.DecodeExact
	switch {
	case delta < -r.jitter && !fromPrevEpoch:
		e := model.NewError(r.track, model.StageClock, model.CodeBackwardTimestamp, false,
			"timestamp stepped back %d ticks (beyond %d-tick jitter): %d -> %d", -delta, r.jitter, r.lastExt, ext)
		r.record(DecisionBackward, raw, ext, false, e.Message)
		return 0, model.DecodeRejected, e
	case delta < 0:
		r.record(DecisionReorder, raw, ext, true,
			fmt.Sprintf("out-of-order timestamp %d ticks before the running maximum", -delta))
		state = model.DecodeForwarded
	case delta == 0:
		r.record(DecisionReorder, raw, ext, true, "duplicate timestamp forwarded")
		state = model.DecodeDuplicated
	}

	r.lastRaw = raw
	r.epoch = epoch
	if ext > r.lastExt {
		r.lastExt = ext
	}
	r.seen++
	return ext, state, nil
}

func (r *Repairer) record(kind DecisionKind, raw, rep int64, recoverable bool, msg string) {
	r.decisions = append(r.decisions, Decision{
		Track: r.track, Kind: kind, RawTicks: raw, Repaired: rep, Epoch: r.epoch,
		Recoverable: recoverable, Message: msg,
	})
}

func (r *Repairer) rawTime(raw int64) model.RationalTime {
	t, err := FromTicks(raw, r.timescale)
	if err != nil {
		return model.RationalTime{Num: raw, Den: int64(r.timescale)}
	}
	return ToModel(t)
}

// TimeAt converts an extended tick position (relative to the anchor) into a
// model timestamp.
func (r *Repairer) TimeAt(extTicks int64) model.RationalTime {
	return ticksToTime(extTicks-r.anchor, r.timescale)
}

// DurationTicks converts a tick count to exact seconds.
func (r *Repairer) DurationTicks(ticks int64) model.RationalTime {
	return ticksToTime(ticks, r.timescale)
}

func ticksToTime(ticks int64, timescale uint32) model.RationalTime {
	t, err := FromTicks(ticks, timescale)
	if err != nil {
		return model.RationalTime{Num: ticks, Den: int64(timescale)}
	}
	return ToModel(t)
}

// ToModel converts an exact rational into its serializable form.
func ToModel(r Rational) model.RationalTime {
	r = r.canon()
	return model.RationalTime{Num: r.num, Den: r.den}
}

// FromModel converts a serialized rational back to an exact value.
func FromModel(t model.RationalTime) (Rational, error) {
	return New(t.Num, t.Den)
}

// Anchor is one track's first extended timestamp, used by the fan-in stage to
// pick a common origin. Timescale is the track clock.
type Anchor struct {
	Track      model.TrackID
	FirstTicks int64
	Timescale  uint32
}

// CommonOrigin returns the earliest track start on the program clock. Tracks
// that start later are shifted forward relative to it by SetAnchor.
func CommonOrigin(anchors []Anchor) (Rational, error) {
	if len(anchors) == 0 {
		return Zero, nil
	}
	sorted := make([]Anchor, len(anchors))
	copy(sorted, anchors)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Track != sorted[j].Track {
			return sorted[i].Track < sorted[j].Track
		}
		return sorted[i].FirstTicks < sorted[j].FirstTicks
	})
	best, err := FromTicks(sorted[0].FirstTicks, sorted[0].Timescale)
	if err != nil {
		return Zero, err
	}
	for _, a := range sorted[1:] {
		cur, err := FromTicks(a.FirstTicks, a.Timescale)
		if err != nil {
			return Zero, err
		}
		if cur.Less(best) {
			best = cur
		}
	}
	return best, nil
}

// TicksAt converts an exact program time into ticks at timescale.
func TicksAt(t Rational, timescale uint32) (int64, error) {
	return t.FloorTicks(timescale)
}
