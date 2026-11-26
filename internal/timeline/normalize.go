// Package timeline normalizes every parsed track and timed event onto the
// canonical nanosecond time base, preserves ordering anomalies verbatim and
// builds a stable, content-addressed index for the cache layer.
package timeline

import (
	"fmt"
	"sort"

	"mediad/internal/model"
	"mediad/internal/scan"
)

// NormalizedSample is a scan.Sample whose tick has been converted exactly to
// canonical nanoseconds while retaining the source byte range.
type NormalizedSample struct {
	TrackID    uint32 `json:"track_id"`
	StartNanos int64  `json:"start_nanos"`
	EndNanos   int64  `json:"end_nanos"`
	Offset     int64  `json:"offset"`
	Length     int64  `json:"length"`
	IsSync     bool   `json:"is_sync"`
}

// NormalizedEvent is a timed event in canonical nanoseconds; the original tick
// and time base are retained so anomalies are never silently rewritten.
type NormalizedEvent struct {
	scan.TimedEvent
	StartNanos int64 `json:"start_nanos"`
	EndNanos   int64 `json:"end_nanos"`
}

// NormalizedTrack pairs a track summary with its normalized samples/events.
type NormalizedTrack struct {
	scan.TrackSummary
	Samples []NormalizedSample `json:"samples"`
	Events  []NormalizedEvent  `json:"events"`
}

// Normalized is the full source timeline on one time base.
type Normalized struct {
	SourceHash string            `json:"source_hash"`
	SourcePath string            `json:"source_path"`
	Tracks     []NormalizedTrack `json:"tracks"`
	Anomalies  []model.Anomaly   `json:"anomalies"`
}

// Normalize converts a scan result to canonical nanoseconds. Conversion
// overflow produces a TS_OVERFLOW anomaly that keeps the raw tick rather than
// dropping or clamping the value.
func Normalize(res *scan.ScanResult) (*Normalized, error) {
	if res == nil {
		return nil, fmt.Errorf("timeline: nil scan result")
	}
	out := &Normalized{SourceHash: res.SourceHash, SourcePath: res.SourcePath}
	out.Anomalies = append(out.Anomalies, res.Anomalies...)

	// Track each track by its slice index rather than by element pointer:
	// later appends can reallocate the backing array and invalidate pointers.
	byTrack := map[uint32]int{}
	for i := range res.Tracks {
		ts := res.Tracks[i]
		out.Tracks = append(out.Tracks, NormalizedTrack{TrackSummary: ts})
		byTrack[ts.ID] = len(out.Tracks) - 1
	}
	ensureVirtual(out, byTrack)

	for _, s := range res.Samples {
		idx := byTrack[s.TrackID]
		t := &out.Tracks[idx]
		base := t.TimeBase
		start, err := base.ToNanos(s.Tick)
		if err != nil {
			out.Anomalies = append(out.Anomalies, overflowAnomaly(s.TrackID, s.Offset, base, s.Tick))
			continue
		}
		end, err := base.ToNanos(s.Tick + s.DurTick)
		if err != nil {
			end = start
		}
		t.Samples = append(t.Samples, NormalizedSample{
			TrackID: s.TrackID, StartNanos: start, EndNanos: end,
			Offset: s.Offset, Length: s.Length, IsSync: s.IsSync,
		})
	}

	for _, ev := range res.Events {
		idx := byTrack[ev.TrackID]
		t := &out.Tracks[idx]
		start, err := ev.TimeBase.ToNanos(ev.Tick)
		if err != nil {
			out.Anomalies = append(out.Anomalies, overflowAnomaly(ev.TrackID, ev.Offset, ev.TimeBase, ev.Tick))
			continue
		}
		end, err := ev.TimeBase.ToNanos(ev.Tick + ev.DurTick)
		if err != nil {
			end = start
		}
		t.Events = append(t.Events, NormalizedEvent{TimedEvent: ev, StartNanos: start, EndNanos: end})
	}

	sort.SliceStable(out.Anomalies, anomalyLess(out.Anomalies))
	return out, nil
}

func ensureVirtual(out *Normalized, byTrack map[uint32]int) {
	if _, ok := byTrack[0]; !ok {
		out.Tracks = append(out.Tracks, NormalizedTrack{
			TrackSummary: scan.TrackSummary{ID: 0, Kind: scan.KindMetadata, Handler: "virtual"},
		})
		byTrack[0] = len(out.Tracks) - 1
	}
}

func overflowAnomaly(trackID uint32, off int64, base model.Rational, tick int64) model.Anomaly {
	return model.Anomaly{
		Type: model.AnomalyTimestampOverflow, TrackID: trackID,
		Message: "timestamp conversion overflowed", SourceOffset: off,
		TimeBase: base, RawTick: tick, Recovery: model.StateOpen,
	}
}

// anomalyLess defines the manifest's stable order: track, then start time,
// then source offset, then type. Identical entries keep insertion order.
func anomalyLess(a []model.Anomaly) func(i, j int) bool {
	return func(i, j int) bool {
		return a[i].StableKey() < a[j].StableKey()
	}
}

// InRange returns the samples and events of one track intersecting [from,to].
func (n *Normalized) InRange(trackID uint32, from, to int64) ([]NormalizedSample, []NormalizedEvent) {
	for _, t := range n.Tracks {
		if t.ID != trackID {
			continue
		}
		var ss []NormalizedSample
		for _, s := range t.Samples {
			if s.EndNanos >= from && s.StartNanos <= to {
				ss = append(ss, s)
			}
		}
		var evs []NormalizedEvent
		for _, e := range t.Events {
			if e.EndNanos >= from && e.StartNanos <= to {
				evs = append(evs, e)
			}
		}
		return ss, evs
	}
	return nil, nil
}
