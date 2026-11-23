package inspect

import (
	"sort"

	"archivecheck/internal/model"
)

// KeyframeInterval is one measured distance between consecutive sync samples.
type KeyframeInterval struct {
	// TrackID is the track the interval belongs to.
	TrackID uint32 `json:"track_id"`
	// FromSample and ToSample are the 0-based sample indices.
	FromSample int `json:"from_sample"`
	ToSample   int `json:"to_sample"`
	// Ticks is the interval in the track timescale.
	Ticks uint64 `json:"ticks"`
	// Start is the interval origin on the shared timeline.
	Start int64 `json:"start"`
}

// KeyframeReport summarises sync-sample cadence for one fragment.
type KeyframeReport struct {
	// Intervals are ordered by track then start time.
	Intervals []KeyframeInterval `json:"intervals"`
	// P50Ms is the median interval in milliseconds, exact, expressed as a
	// numerator over 1000. It is -1 when no interval exists.
	P50Ms int64 `json:"p50_ms"`
	// P50Remainder is the fractional millisecond of the median.
	P50Remainder model.Rational `json:"p50_remainder"`
}

// milliseconds converts a tick interval in the given media timescale to an
// exact millisecond value without floating point.
func milliseconds(ticks uint64, timescale uint32) (model.Time, error) {
	if ticks > 1<<52 {
		return model.Time{}, model.ErrTimeOverflow
	}
	return model.ConvertTicks(int64(ticks), int64(timescale), 1000)
}

// KeyframeIntervals computes the sync-sample interval distribution for a
// fragment. Track timescales are converted to the shared timebase with exact
// remainders; the median is computed on the exact tick values.
func KeyframeIntervals(f *Fragment, timebase int64) KeyframeReport {
	rep := KeyframeReport{P50Ms: -1, P50Remainder: model.Rational{Den: 1}}
	if timebase <= 0 {
		return rep
	}
	type exact struct {
		interval KeyframeInterval
		ms       model.Time
	}
	var all []exact
	for _, tr := range f.Tracks {
		if tr.Timescale == 0 || len(tr.SyncSamples) < 2 {
			continue
		}
		for i := 1; i < len(tr.SyncSamples); i++ {
			prev, cur := tr.SyncSamples[i-1], tr.SyncSamples[i]
			if prev >= len(tr.Samples) || cur >= len(tr.Samples) {
				continue
			}
			ticks := tr.Samples[cur].DecodeTime - tr.Samples[prev].DecodeTime
			ms, err := milliseconds(ticks, tr.Timescale)
			if err != nil {
				continue
			}
			absStart, err := model.ConvertTicks(int64(tr.Samples[prev].DecodeTime), int64(tr.Timescale), timebase)
			if err != nil {
				continue
			}
			all = append(all, exact{
				interval: KeyframeInterval{
					TrackID:    tr.TrackID,
					FromSample: prev,
					ToSample:   cur,
					Ticks:      ticks,
					Start:      absStart.Ticks,
				},
				ms: ms,
			})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].interval.TrackID != all[j].interval.TrackID {
			return all[i].interval.TrackID < all[j].interval.TrackID
		}
		return all[i].interval.Start < all[j].interval.Start
	})
	for _, e := range all {
		rep.Intervals = append(rep.Intervals, e.interval)
	}
	if len(all) == 0 {
		return rep
	}
	ms := make([]model.Time, len(all))
	for i, e := range all {
		ms[i] = e.ms
	}
	median := model.FrameDuration(ms)
	rep.P50Ms = median.Ticks
	rep.P50Remainder = median.Rem
	return rep
}
