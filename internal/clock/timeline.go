// Package clock provides the shared nanosecond time base used by every stage
// of the pipeline.
//
// Ingest devices timestamp blocks with a 32-bit millisecond counter that
// wraps roughly every 49.7 days and advances at a slightly different rate on
// every device. Timeline unwraps the counter, estimates each track's rate
// against the nominal block cadence declared by the container, and maps all
// tracks onto the reference track's clock. The corrected output is strictly
// increasing per track, so downstream index and fan-in code can rely on
// ordering without re-checking it.
package clock

import (
	"sort"

	"mediad/internal/media"
)

// WrapPeriodMs is the period of the 32-bit millisecond timestamp counter.
const WrapPeriodMs int64 = 1 << 32

// NanosPerMs converts milliseconds to nanoseconds.
const NanosPerMs int64 = 1e6

// Options tunes calibration behaviour. The zero value is valid.
type Options struct {
	// MaxDriftPPM clamps the estimated rate deviation. Default 5000 (0.5%).
	MaxDriftPPM int64
	// MinSamples is the minimum number of timed observations needed to
	// estimate a rate. Default 8.
	MinSamples int
	// MaxSamples caps retained fit samples per track to bound memory.
	// Default 4096.
	MaxSamples int
}

func (o Options) withDefaults() Options {
	if o.MaxDriftPPM <= 0 {
		o.MaxDriftPPM = 5000
	}
	if o.MinSamples <= 0 {
		o.MinSamples = 8
	}
	if o.MaxSamples <= 0 {
		o.MaxSamples = 4096
	}
	return o
}

// Observation is one block timestamp seen during a linear read of the stream.
type Observation struct {
	Track media.TrackID
	// RawMs is the container timestamp in milliseconds before unwrapping.
	RawMs uint32
	// NominalNs is the block's nominal duration from container metadata.
	// Zero means the duration is unknown; such observations still unwrap but
	// do not contribute to rate estimation.
	NominalNs int64
}

type sample struct {
	nominalNs int64
	rawNs     int64
}

type anchor struct {
	rawNs    int64
	sharedNs int64
}

type trackClock struct {
	id   media.TrackID
	opts Options

	sawRaw      bool
	lastRaw     uint32
	lastUnwr    int64 // last unwrapped value in nanoseconds
	wraps       int64
	firstRawNs  int64
	haveFirstNs bool

	nominalNs int64
	samples   []sample

	slope float64 // raw nanoseconds per true nanosecond

	anchors   []anchor
	baseRawNs int64
	baseShare int64
	haveShare bool

	scale      float64
	lastOut    int64
	haveOutput bool
}

// Timeline maps per-track raw timestamps onto one shared clock.
type Timeline struct {
	ref        media.TrackID
	opts       Options
	tracks     map[media.TrackID]*trackClock
	calibrated bool
}

// New returns a Timeline whose shared clock is the given reference track.
func New(reference media.TrackID, opts Options) *Timeline {
	return &Timeline{
		ref:    reference,
		opts:   opts.withDefaults(),
		tracks: make(map[media.TrackID]*trackClock),
	}
}

// Reference returns the reference track id.
func (tl *Timeline) Reference() media.TrackID { return tl.ref }

func (tl *Timeline) track(id media.TrackID) *trackClock {
	tr, ok := tl.tracks[id]
	if !ok {
		tr = &trackClock{id: id, opts: tl.opts}
		tl.tracks[id] = tr
	}
	return tr
}

// unwrap extends the 32-bit millisecond counter to 64 bits.
//
// A backwards jump larger than half the wrap period is a counter wrap and
// advances the epoch. Smaller backwards steps are device jitter and are
// clamped, so the unwrapped value never decreases.
func (tr *trackClock) unwrap(raw uint32) int64 {
	if !tr.sawRaw {
		tr.sawRaw = true
		tr.lastRaw = raw
		tr.lastUnwr = int64(raw) * NanosPerMs
		return tr.lastUnwr
	}
	if int64(tr.lastRaw)-int64(raw) > WrapPeriodMs/2 {
		tr.wraps++
		tr.lastRaw = raw
	} else if raw >= tr.lastRaw {
		tr.lastRaw = raw
	}
	// else: small backwards step, keep the last epoch boundary and count.
	cand := (int64(raw) + tr.wraps*WrapPeriodMs) * NanosPerMs
	if cand < tr.lastUnwr {
		cand = tr.lastUnwr
	}
	tr.lastUnwr = cand
	return cand
}

// Observe records one block timestamp and returns its unwrapped nanoseconds.
// Observations must be supplied in stream order per track.
func (tl *Timeline) Observe(o Observation) (rawNs int64) {
	tr := tl.track(o.Track)
	rawNs = tr.unwrap(o.RawMs)
	if !tr.haveFirstNs {
		tr.firstRawNs = rawNs
		tr.haveFirstNs = true
	}
	if o.NominalNs > 0 {
		if len(tr.samples) < tr.opts.MaxSamples {
			tr.samples = append(tr.samples, sample{
				nominalNs: tr.nominalNs,
				rawNs:     rawNs - tr.firstRawNs,
			})
		}
		tr.nominalNs += o.NominalNs
	}
	return rawNs
}

// Anchor pins a track's raw timestamp to a known shared-clock instant. It is
// optional: without anchors tracks are aligned on their first observation.
func (tl *Timeline) Anchor(t media.TrackID, rawNs, sharedNs int64) {
	tr := tl.track(t)
	tr.anchors = append(tr.anchors, anchor{rawNs: rawNs, sharedNs: sharedNs})
	sort.SliceStable(tr.anchors, func(i, j int) bool { return tr.anchors[i].rawNs < tr.anchors[j].rawNs })
	if !tr.haveFirstNs {
		tr.firstRawNs = rawNs
		tr.haveFirstNs = true
	}
}
