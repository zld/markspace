// Package sync measures cross-track (A/V) deviation on the master timeline.
// Deviations beyond the tolerance are recorded as errors; missing
// counterparts are reported, never filled with fabricated samples.
package sync

import (
	"sort"

	"archive/internal/model"
)

// DefaultToleranceNs is the default A/V sync tolerance: 20ms.
const DefaultToleranceNs int64 = 20_000_000

// maxPairDistanceNs caps how far apart two packets may be to count as
// counterparts; beyond this there is simply no counterpart to compare with.
const maxPairDistanceNs int64 = 500_000_000

// historyCap bounds the per-track timestamp history. It only needs to cover
// the maximum emission skew between the two tracks (the jitter window).
const historyCap = 256

// Tracker measures each packet against its nearest counterpart on the other
// track. Because the jitter buffer can hold one track back while the other
// advances, a packet is only judged once the other track has caught up
// (or at Finalize); until then it waits in a bounded pending queue.
type Tracker struct {
	ToleranceNs int64

	pendV, pendA []int64 // emitted but not yet judgeable (monotonic)
	histV, histA []int64 // recent emitted timestamps (monotonic, capped)

	MaxDevNs   int64
	Violations int
	Errs       model.Errors
}

func NewTracker(toleranceNs int64) *Tracker {
	if toleranceNs <= 0 {
		toleranceNs = DefaultToleranceNs
	}
	return &Tracker{ToleranceNs: toleranceNs}
}

// Observe registers one emitted packet. Per-track emission is PTS-ordered.
func (t *Tracker) Observe(trackType string, ptsNs int64) {
	if trackType == "video" {
		t.pendV = append(t.pendV, ptsNs)
		t.histV = appendHist(t.histV, ptsNs)
		t.resolve(&t.pendV, t.histA)
	} else {
		t.pendA = append(t.pendA, ptsNs)
		t.histA = appendHist(t.histA, ptsNs)
		t.resolve(&t.pendA, t.histV)
	}
}

// Finalize judges every still-pending packet against the final state of the
// other track. Call at end of stream (and before checkpointing an interrupt).
func (t *Tracker) Finalize() {
	for _, p := range t.pendV {
		t.record(nearestDist(t.histA, p))
	}
	for _, p := range t.pendA {
		t.record(nearestDist(t.histV, p))
	}
	t.pendV, t.pendA = nil, nil
}

// resolve judges pending packets once the other track's history contains a
// timestamp at or beyond them: the nearest counterpart is then known for
// certain, because later arrivals can only be further away.
func (t *Tracker) resolve(pend *[]int64, other []int64) {
	for len(*pend) > 0 {
		p := (*pend)[0]
		if len(other) == 0 || other[len(other)-1] < p {
			return
		}
		t.record(nearestDist(other, p))
		*pend = (*pend)[1:]
	}
}

func (t *Tracker) record(dev int64) {
	if dev < 0 || dev > maxPairDistanceNs {
		return // no usable counterpart; nothing fabricated
	}
	if dev > t.MaxDevNs {
		t.MaxDevNs = dev
	}
	if dev > t.ToleranceNs {
		t.Violations++
		t.Errs = append(t.Errs, model.ScanError{
			Code:   model.ErrAVDesync,
			Offset: -1,
			Track:  -1,
			Detail: "A/V deviation " + model.Timestamp{Nanos: dev}.String() +
				" exceeds tolerance " + model.Timestamp{Nanos: t.ToleranceNs}.String(),
		})
	}
}

// nearestDist returns the distance to the nearest timestamp in the sorted
// slice, or -1 when the slice is empty.
func nearestDist(sorted []int64, p int64) int64 {
	if len(sorted) == 0 {
		return -1
	}
	i := sort.Search(len(sorted), func(i int) bool { return sorted[i] >= p })
	best := int64(-1)
	if i < len(sorted) {
		best = sorted[i] - p
	}
	if i > 0 {
		if d := p - sorted[i-1]; best < 0 || d < best {
			best = d
		}
	}
	return best
}

func appendHist(hist []int64, pts int64) []int64 {
	hist = append(hist, pts)
	if len(hist) > historyCap {
		hist = append([]int64(nil), hist[len(hist)-historyCap:]...)
	}
	return hist
}
