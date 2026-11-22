// Package timeline owns the recomputable shared time line: multi-track clock
// mapping, a watermark that advances only on observed events, and
// checkpoint-based deterministic replay of late data.
package timeline

import (
	"sort"

	"mediacore"
	"mediacore/syncer"
)

// ClockRegistry maps each track's device/ RTP clock onto the shared integer
// master-nanosecond time base. RTP timestamps wrap at 2^32; the registry
// unwraps them relative to each track's first observed anchor.
type ClockRegistry struct {
	tracks map[mediacore.TrackID]*trackClock
}

type trackClock struct {
	rate      uint32
	wrapBase  uint64 // extended RTP timestamp of the first observed packet
	wrapInit  bool
	lastTS    uint64
	est       *syncer.DriftEstimator
	originDev int64 // device ns corresponding to master origin
	originMas int64
	anchored  bool
}

// NewClockRegistry creates an empty registry.
func NewClockRegistry() *ClockRegistry {
	return &ClockRegistry{tracks: make(map[mediacore.TrackID]*trackClock)}
}

// Register adds a track with its RTP clock rate and ppm bound.
func (r *ClockRegistry) Register(id mediacore.TrackID, clockRate uint32, maxPPM int64) {
	r.tracks[id] = &trackClock{rate: clockRate, est: syncer.NewDriftEstimator(id, maxPPM)}
}

// Anchor binds an extended RTP timestamp to a master-clock time and records a
// drift sample. Anchors are the only source of clock origin information.
func (r *ClockRegistry) Anchor(id mediacore.TrackID, rtpTS uint64, masterNS int64) error {
	tc, ok := r.tracks[id]
	if !ok {
		return mediacore.NewError(mediacore.CatLayout, id, "clock.register", 0, "unregistered track")
	}
	device := rtpToNS(rtpTS, tc.rate)
	if !tc.anchored {
		tc.originDev, tc.originMas = device, masterNS
		tc.anchored = true
	}
	tc.est.Observe(syncer.Anchor{Device: device, Master: masterNS - tc.originMas})
	return nil
}

// Map unwraps a raw 32-bit RTP timestamp and converts it to master-clock
// nanoseconds through the track's drift-corrected rational transform. The
// original device time is returned alongside so callers never lose it.
func (r *ClockRegistry) Map(id mediacore.TrackID, raw uint32, recvNS int64) (mapped, orig int64, alignable bool, err error) {
	tc, ok := r.tracks[id]
	if !ok {
		return 0, 0, false, mediacore.NewError(mediacore.CatLayout, id, "clock.map", 0, "unregistered track")
	}
	ext := unwrap32(tc, raw)
	device := rtpToNS(ext, tc.rate)
	orig = device
	if !tc.anchored {
		// No anchor yet: fall back to receive time but flag non-anchored use.
		return recvNS, orig, true, nil
	}
	local, alignable := tc.est.Map(device - tc.originDev)
	return tc.originMas + local, orig, alignable, nil
}

// Alignable reports a track's drift state.
func (r *ClockRegistry) Alignable(id mediacore.TrackID) (bool, error) {
	tc, ok := r.tracks[id]
	if !ok {
		return false, mediacore.NewError(mediacore.CatLayout, id, "clock.state", 0, "unregistered track")
	}
	return tc.est.Alignable(), nil
}

// Estimator exposes a track's drift estimator for reporting.
func (r *ClockRegistry) Estimator(id mediacore.TrackID) (*syncer.DriftEstimator, bool) {
	tc, ok := r.tracks[id]
	return tc.est, ok
}

// rtpToNS converts an extended RTP timestamp to integer device nanoseconds.
func rtpToNS(ts uint64, rate uint32) int64 {
	if rate == 0 {
		return 0
	}
	return int64(mulDivNS(ts, uint64(mediacore.Second), uint64(rate)))
}

// mulDivNS computes a*b/c for non-negative operands with 128-bit precision.
func mulDivNS(a, b, c uint64) uint64 {
	ah, al := a>>32, a&0xffffffff
	bh, bl := b>>32, b&0xffffffff
	lo := al * bl
	mid := ah*bl + al*bh
	hi := ah*bh + mid>>32
	low := lo + mid<<32
	if low < lo {
		hi++
	}
	return divu128(hi, low, c)
}

func divu128(hi, lo, c uint64) uint64 {
	if hi == 0 {
		return lo / c
	}
	var rem, q uint64
	for i := 63; i >= 0; i-- {
		rem = rem<<1 | (hi>>uint(i))&1
		if rem >= c {
			rem -= c
			q |= 1 << uint(i)
		}
	}
	qHi := q
	rem, q = 0, 0
	for i := 63; i >= 0; i-- {
		rem = rem<<1 | (lo>>uint(i))&1
		if rem >= c {
			rem -= c
			q |= 1 << uint(i)
		}
	}
	return qHi<<32 | q
}

// unwrap32 extends a raw RTP timestamp to 64 bits using shortest signed
// distance from the last extended timestamp.
func unwrap32(tc *trackClock, raw uint32) uint64 {
	if !tc.wrapInit {
		tc.wrapBase = uint64(raw)
		tc.lastTS = uint64(raw)
		tc.wrapInit = true
		return tc.lastTS
	}
	high := tc.lastTS &^ uint64(0xffffffff)
	cand := high | uint64(raw)
	if d := int64(cand - tc.lastTS); d > (1<<31) {
		cand -= 1 << 32
	} else if d < -(1 << 31) {
		cand += 1 << 32
	}
	tc.lastTS = cand
	return cand
}

// SortedTracks returns registered track IDs in deterministic order.
func (r *ClockRegistry) SortedTracks() []mediacore.TrackID {
	ids := make([]string, 0, len(r.tracks))
	for id := range r.tracks {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	out := make([]mediacore.TrackID, len(ids))
	for i, id := range ids {
		out[i] = mediacore.TrackID(id)
	}
	return out
}
