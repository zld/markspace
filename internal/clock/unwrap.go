package clock

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"avbatch/internal/mediaerr"
)

// RTP timestamps are 32-bit and wrap every 2^32/clockrate seconds (about 13
// hours at 90 kHz). Unwrapper keeps a full 64-bit epoch so one session can run
// for years.
//
// The expansion uses the signed modular difference int32(cur-prev). That is
// exact for any step smaller than half the timestamp space, which covers both
// forward and backward wrap without a special case, and it makes anomalies
// observations rather than guesses: a backwards step is reported, never
// rewritten into a plausible-looking forward one.

// WrapResult is one expanded timestamp.
type WrapResult struct {
	// Input is the original 32-bit timestamp.
	Input uint32
	// Extended is the expanded 64-bit tick count.
	Extended uint64
	// Epoch is Extended>>32, i.e. how many times the space wrapped.
	Epoch uint64
	// Wrapped reports that this sample crossed a wrap boundary.
	Wrapped bool
	// Backwards reports a backwards step beyond the configured tolerance.
	Backwards bool
	// Delta is the signed step from the previous sample.
	Delta int64
}

// Unwrapper expands RTP timestamps into monotonic 64-bit ticks.
type Unwrapper struct {
	prev        uint32
	prevExt     int64
	havePrev    bool
	tolerance   uint32
	clockRate   uint32
	anomalies   int
	firstErr    error
	wraps       int
	maxBackstep uint32
}

// NewUnwrapper creates an unwrapper. tolerance is how many ticks a stream may
// legitimately step backwards (network reordering); zero means that any
// backwards step is an anomaly. clockRate is used for nanosecond conversion
// and anomaly messages.
func NewUnwrapper(tolerance uint32, clockRate uint32) *Unwrapper {
	return &Unwrapper{tolerance: tolerance, clockRate: clockRate}
}

// Reset forgets state; the next Push starts a fresh session.
func (u *Unwrapper) Reset() {
	u.prev, u.prevExt, u.havePrev = 0, 0, false
	u.anomalies, u.firstErr, u.wraps, u.maxBackstep = 0, nil, 0, 0
}

// SetPrevious primes the unwrapper at an already-expanded baseline, used when
// resuming mid-stream.
func (u *Unwrapper) SetPrevious(ts uint32, extended uint64) error {
	if extended > 1<<63-1 {
		return fmt.Errorf("clock: expanded timestamp %d out of range", extended)
	}
	u.prev, u.prevExt, u.havePrev = ts, int64(extended), true
	return nil
}

// Push expands one timestamp. It always returns a result; callers that want
// strict ordering check Err.
func (u *Unwrapper) Push(ts uint32) WrapResult {
	if !u.havePrev {
		u.prev, u.prevExt, u.havePrev = ts, int64(ts), true
		return WrapResult{Input: ts, Extended: uint64(ts)}
	}
	delta := int64(int32(ts - u.prev))
	ext := u.prevExt + delta
	if ext < 0 {
		// The stream moved further back than the current epoch allows; clamp
		// at zero and report, which is the only representation-preserving
		// option.
		u.note(fmt.Sprintf("rtp timestamp 0x%08x expanded below zero", ts))
		ext = 0
	}
	res := WrapResult{
		Input:    ts,
		Extended: uint64(ext),
		Epoch:    uint64(ext) >> 32,
		Delta:    delta,
		Wrapped:  delta > 0 && (uint64(ext)>>32) != (uint64(u.prevExt)>>32),
	}
	if delta < 0 {
		back := uint32(-delta)
		if back > u.tolerance {
			res.Backwards = true
			if back > u.maxBackstep {
				u.maxBackstep = back
			}
			u.note(fmt.Sprintf("rtp timestamp regressed by %d ticks (0x%08x after 0x%08x)", back, ts, u.prev))
		}
	}
	if res.Wrapped {
		u.wraps++
	}
	u.prev, u.prevExt = ts, ext
	return res
}

func (u *Unwrapper) note(msg string) {
	u.anomalies++
	if u.firstErr == nil {
		u.firstErr = mediaerr.New(mediaerr.KindRTPRegression, -1, "rtp", int64(u.prev), msg)
	}
}

// Anomalies returns how many backwards steps were observed.
func (u *Unwrapper) Anomalies() int { return u.anomalies }

// Wraps returns how many wrap boundaries were crossed.
func (u *Unwrapper) Wraps() int { return u.wraps }

// MaxBackstep returns the largest backwards step in ticks.
func (u *Unwrapper) MaxBackstep() uint32 { return u.maxBackstep }

// Err returns the first anomaly, or nil.
func (u *Unwrapper) Err() error { return u.firstErr }

// Nanos converts an extended tick count at the unwrapper's clock rate.
func (u *Unwrapper) Nanos(extended uint64) (int64, error) {
	if u.clockRate == 0 {
		return 0, errors.New("clock: zero rtp clock rate")
	}
	return Timebase{Timescale: u.clockRate}.TicksToNanos(extended)
}

// UnwrapAll expands a whole timestamp run and returns the first anomaly.
func UnwrapAll(timestamps []uint32, tolerance uint32, clockRate uint32) ([]uint64, error) {
	u := NewUnwrapper(tolerance, clockRate)
	out := make([]uint64, len(timestamps))
	for i, ts := range timestamps {
		out[i] = u.Push(ts).Extended
	}
	return out, u.Err()
}

// SortByExtended stably sorts indices by expanded timestamp, refusing to order
// two packets that expanded to the same value.
func SortByExtended(ext []uint64) ([]int, error) {
	idx := make([]int, len(ext))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool { return ext[idx[i]] < ext[idx[j]] })
	var dup []error
	for i := 1; i < len(idx); i++ {
		if ext[idx[i]] == ext[idx[i-1]] {
			dup = append(dup, mediaerr.New(mediaerr.KindUnsorted, -1, "rtp", int64(ext[idx[i]]),
				"two rtp packets share an expanded timestamp"))
		}
	}
	return idx, errors.Join(dup...)
}

// ArrivalDrift estimates sender clock error from packet arrival times. It is a
// sanity bound only: networks reorder and burst, so the result is clamped and
// never used to rewrite media time.
func ArrivalDrift(ext []uint64, arrivals []time.Time, nominalRate uint32) float64 {
	if len(ext) < 2 || len(ext) != len(arrivals) || nominalRate == 0 {
		return 0
	}
	type pair struct {
		t  uint64
		at int64
	}
	ps := make([]pair, len(ext))
	for i := range ext {
		ps[i] = pair{t: ext[i], at: arrivals[i].UnixNano()}
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].t < ps[j].t })
	t0, w0 := ps[0].t, ps[0].at
	for _, p := range ps {
		dt := float64(p.t-t0) / float64(nominalRate)
		dw := float64(p.at-w0) / 1e9
		if dt > 1 {
			ppm := (dw/dt - 1) * 1e6
			if ppm > MaxPPM {
				return MaxPPM
			}
			if ppm < -MaxPPM {
				return -MaxPPM
			}
			return ppm
		}
	}
	return 0
}
