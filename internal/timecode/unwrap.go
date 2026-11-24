package timecode

import (
	"fmt"

	"mediaaudit/internal/model"

	"github.com/pion/rtp"
)

// Unwrapper extends a wrapping timestamp field (an RTP 32-bit timestamp or a
// container field) into a monotonically increasing 64-bit counter.
//
// The algorithm is the standard "half-range" rule: a step that looks like a
// backwards jump of more than half the modulus is reinterpreted as a wrap
// forward, so 2^32-100 -> 50 becomes one continuous clock.
type Unwrapper struct {
	Modulus uint64
	epoch   uint64
	last    uint64
	started bool
	wraps   int
}

// NewUnwrapper builds an unwrapper for a field of the given bit width
// (32 for RTP, up to 63 for container fields).
func NewUnwrapper(bits uint) (*Unwrapper, error) {
	if bits == 0 || bits > 63 {
		return nil, model.NewError(model.CodeInvalidArgument, "timecode", "", model.InputPos{},
			"timestamp width must be 1..63 bits, got %d", bits)
	}
	return &Unwrapper{Modulus: uint64(1) << bits}, nil
}

// Extend converts one raw timestamp. It reports whether a wrap was detected
// at this step and the total number of wraps observed so far.
//
// Forward steps are always taken at face value: a monotonic stream may jump.
// A backwards step is a wrap only when the implied forward distance is at
// most half the modulus; otherwise it is a reordered or duplicate packet, and
// the clock is left untouched (including its anchor) so later in-order
// timestamps are still measured from the true latest value.
func (u *Unwrapper) Extend(raw uint64) (ext uint64, wrapped bool, epoch int) {
	if u.Modulus == 0 {
		u.Modulus = 1 << 32
	}
	raw %= u.Modulus
	if !u.started {
		u.started = true
		u.last = raw
		u.epoch = raw
		return raw, false, 0
	}
	if raw >= u.last {
		u.epoch += raw - u.last
		u.last = raw
		return u.epoch, false, u.wraps
	}
	delta := u.Modulus - u.last + raw
	if delta > u.Modulus/2 {
		// Reordered/duplicate: do not move the anchor.
		return u.epoch, false, u.wraps
	}
	u.wraps++
	u.epoch += delta
	u.last = raw
	return u.epoch, true, u.wraps
}

// Wraps returns the number of wraps observed.
func (u *Unwrapper) Wraps() int { return u.wraps }

// RTPTimestamp extracts the timestamp from a parsed RTP packet.
func RTPTimestamp(p *rtp.Packet) uint32 {
	if p == nil {
		return 0
	}
	return p.Timestamp
}

// ExtendRTPStream unwraps the timestamps of a parsed RTP stream and returns
// the extended values plus the wrap positions. Packets are not modified.
func ExtendRTPStream(pkts []*rtp.Packet) ([]uint64, []int, error) {
	if len(pkts) == 0 {
		return nil, nil, nil
	}
	u, err := NewUnwrapper(32)
	if err != nil {
		return nil, nil, err
	}
	out := make([]uint64, len(pkts))
	var wraps []int
	for i, p := range pkts {
		if p == nil {
			return nil, nil, model.NewError(model.CodeInvalidArgument, "timecode", "", model.InputPos{},
				"nil RTP packet at index %d", i)
		}
		ext, wrapped, _ := u.Extend(uint64(p.Timestamp))
		out[i] = ext
		if wrapped {
			wraps = append(wraps, i)
		}
	}
	return out, wraps, nil
}

// DriftSample pairs a media-clock instant (derived from the unwrapped
// timestamp) with a reference instant from the master clock.
type DriftSample struct {
	Media model.Rat
	Ref   model.Rat
}

// DriftConfig controls drift acceptance. MaxPPM is the largest tolerated
// relative rate error, in parts per million (500 = 0.05%).
type DriftConfig struct {
	MaxPPM int64
}

// DriftReport is the per-track result of drift estimation.
type DriftReport struct {
	Track      model.Track
	Slope      model.Rat // media seconds per reference second
	Drift      model.Rat // slope - 1
	Correction model.Rat // total correction at the final sample
	MaxPPM     int64
	Exceeded   bool
}

// EstimateDrift fits media = a + b*ref by exact least squares. A slope above
// one means the media clock runs fast; the correction is how much must be
// subtracted from the media timeline to match the reference.
func EstimateDrift(track model.Track, samples []DriftSample, cfg DriftConfig) (DriftReport, error) {
	rep := DriftReport{Track: track, MaxPPM: cfg.MaxPPM}
	if len(samples) < 2 {
		return rep, model.NewError(model.CodeInvalidArgument, "timecode", track.ID, model.InputPos{},
			"drift estimation needs at least 2 samples, got %d", len(samples))
	}
	n := model.RatFromInt(int64(len(samples)))
	var sx, sy, sxx, sxy model.Rat
	for _, s := range samples {
		sx = sx.Add(s.Ref)
		sy = sy.Add(s.Media)
		sxx = sxx.Add(s.Ref.Mul(s.Ref))
		sxy = sxy.Add(s.Ref.Mul(s.Media))
	}
	den := n.Mul(sxx).Sub(sx.Mul(sx))
	if den.IsZero() {
		return rep, model.NewError(model.CodeInvalidArgument, "timecode", track.ID, model.InputPos{},
			"reference times are all identical; slope is undefined")
	}
	num := n.Mul(sxy).Sub(sx.Mul(sy))
	slope, err := num.Quo(den)
	if err != nil {
		return rep, model.NewError(model.CodeInternal, "timecode", track.ID, model.InputPos{}, "%v", err)
	}
	rep.Slope = slope
	rep.Drift = slope.Sub(model.RatFromInt(1))

	first := samples[0]
	last := samples[len(samples)-1]
	mediaSpan := last.Media.Sub(first.Media)
	refSpan := last.Ref.Sub(first.Ref)
	if !refSpan.IsZero() {
		rep.Correction = mediaSpan.Sub(refSpan)
	}
	rep.Exceeded = exceedsPPM(rep.Drift, cfg.MaxPPM)
	return rep, nil
}

func exceedsPPM(drift model.Rat, maxPPM int64) bool {
	if maxPPM < 0 {
		return false // negative disables the check
	}
	limit := model.MustRat(maxPPM, 1_000_000)
	negLimit := model.MustRat(-maxPPM, 1_000_000)
	return drift.Cmp(limit) > 0 || drift.Cmp(negLimit) < 0
}

// DriftError converts an exceeded report into the per-track diagnostic. The
// caller marks only this segment failed and keeps other tracks running.
func (r DriftReport) DriftError() *model.Error {
	if !r.Exceeded {
		return nil
	}
	return model.NewError(model.CodeDriftExceeded, "timecode", r.Track.ID, model.InputPos{},
		"clock drift %s (%s ppm) exceeds limit %d ppm; correction %s s",
		r.Drift, r.PPM(), r.MaxPPM, r.Correction)
}

// PPM renders the drift in parts per million, rounded toward zero.
func (r DriftReport) PPM() string {
	ppm, err := r.Drift.Mul(model.RatFromInt(1_000_000)).Quo(model.RatFromInt(1))
	if err != nil {
		return "0"
	}
	return fmt.Sprintf("%.1f", ppm.Seconds())
}

// CorrectTimeline applies the report's slope correction to raw media times and
// returns a new ordered list. It is deterministic and never mutates input.
func CorrectTimeline(media []model.Rat, rep DriftReport) ([]model.Rat, error) {
	if len(media) == 0 {
		return nil, nil
	}
	out := make([]model.Rat, len(media))
	origin := media[0]
	for i, t := range media {
		// Align the first sample and scale the rest by 1/slope.
		rel := t.Sub(origin)
		corr, err := rel.Quo(rep.Slope)
		if err != nil {
			return nil, model.NewError(model.CodeInternal, "timecode", rep.Track.ID, model.InputPos{},
				"invalid slope %s: %v", rep.Slope, err)
		}
		out[i] = origin.Add(corr)
	}
	return out, nil
}
