// Package clock performs per-representation timestamp recovery: wraparound
// expansion, drift estimation and variable-frame-rate handling. Recovery
// output is always kept separate from the original raw timestamps.
package clock

import (
	"errors"
	"math"

	"github.com/pion/rtp"
)

// halfRange32 is the ambiguity boundary for modular 32-bit timestamps.
const halfRange32 int64 = 1 << 31

// Unwrapper expands monotonically increasing timestamps whose carrier wraps
// around (RTP 32-bit clocks, tfdt version-0 baseMediaDecodeTime). One
// Unwrapper instance belongs to exactly one track/SSRC.
type Unwrapper struct {
	last    uint32
	hasLast bool
	value   int64
	wraps   int
}

// NewUnwrapper32 creates an unwrapper for 32-bit timestamp carriers.
func NewUnwrapper32() *Unwrapper { return &Unwrapper{} }

// Push feeds the next raw timestamp and returns its unwrapped value. A
// backward modular jump larger than half the range is treated as wraparound;
// smaller backward jumps are passed through as negative deltas so genuine
// reordering stays visible instead of being silently rewritten.
func (u *Unwrapper) Push(raw uint32) int64 {
	if !u.hasLast {
		u.hasLast = true
		u.last = raw
		u.value = int64(raw)
		return u.value
	}
	delta := int64(int32(raw - u.last))
	if delta < -halfRange32 {
		// forward wrap, e.g. 0xFFFFFFF0 -> 0x00000010
		delta += 1 << 32
		u.wraps++
	} else if delta > halfRange32 {
		// backward wrap, e.g. 0x00000010 -> 0xFFFFFFF0
		delta -= 1 << 32
		u.wraps++
	}
	u.value += delta
	u.last = raw
	return u.value
}

// WrapCount reports how many carrier wraps have been expanded.
func (u *Unwrapper) WrapCount() int { return u.wraps }

// Reset returns the unwrapper to its initial state.
func (u *Unwrapper) Reset() { *u = Unwrapper{} }

// ExtendRTP unmarshals RTP packets with pion/rtp and expands their 32-bit
// timestamps into a monotonic sequence. The raw packet bytes are the only
// input, matching how an elementary stream arrives after TS demux.
func ExtendRTP(packets [][]byte) (unwrapped []int64, raw []uint32, err error) {
	u := NewUnwrapper32()
	out := make([]int64, 0, len(packets))
	raws := make([]uint32, 0, len(packets))
	for i, buf := range packets {
		var p rtp.Packet
		if e := p.Unmarshal(buf); e != nil {
			return nil, nil, errors.New("rtp packet " + itoa(i) + " unmarshal failed: " + e.Error())
		}
		raws = append(raws, p.Timestamp)
		out = append(out, u.Push(p.Timestamp))
	}
	return out, raws, nil
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	n := len(b)
	for x := i; x > 0; x /= 10 {
		n--
		b[n] = byte('0' + x%10)
	}
	return string(b[n:])
}

// DriftResult summarizes clock behaviour of one track.
type DriftResult struct {
	// DriftPPM is the signed drift of the actual timestamp slope against the
	// nominal sample duration, in parts per million.
	DriftPPM int64
	// NominalDuration is the least-squares nominal per-sample tick duration.
	NominalDuration float64
	// VFR reports whether the stream keeps more than one sample duration.
	VFR bool
}

// EstimateDrift derives drift and the variable-frame-rate flag from unwrapped
// timestamps and the per-sample raw durations. Sample durations are never
// normalized away: VFR content keeps every individual duration.
func EstimateDrift(unwrapped []int64, durations []uint32) DriftResult {
	res := DriftResult{}
	if len(unwrapped) < 2 {
		if len(durations) > 0 {
			res.NominalDuration = float64(durations[0])
		}
		return res
	}
	// VFR: more than one distinct sample duration.
	seen := make(map[uint32]struct{}, len(durations))
	for _, d := range durations {
		seen[d] = struct{}{}
	}
	res.VFR = len(seen) > 1

	// Drift from the total span: the recovered clock span between first and
	// last sample compared with the sum of declared sample durations. This
	// stays valid for VFR content where a per-index slope is dominated by
	// alternating-duration phase on short windows.
	var sumDur float64
	steps := len(unwrapped) - 1
	for i := 0; i < steps && i < len(durations); i++ {
		sumDur += float64(durations[i])
	}
	if len(durations) > 0 {
		res.NominalDuration = math.Round(sumDur/float64(steps)*1000) / 1000
	}
	if sumDur > 0 {
		actual := float64(unwrapped[len(unwrapped)-1] - unwrapped[0])
		res.DriftPPM = int64(math.Round((actual-sumDur)/sumDur * 1e6))
	}
	return res
}

// DriftExceeded reports whether |drift| is above the threshold expressed in
// milliseconds over one second of media (1 ms/s == 1000 PPM).
func DriftExceeded(ppm int64, driftThresholdMsPerSec int64) bool {
	if ppm < 0 {
		ppm = -ppm
	}
	return ppm > driftThresholdMsPerSec*1000
}
