package segment

import (
	"fmt"

	"replaykit/media/audit"
	"replaykit/media/clock"
	"replaykit/media/fault"
)

// FanoutResult holds every event/layer window plus fan-in verification.
type FanoutResult struct {
	Windows      []*Window
	Boundaries   []audit.Boundary
	Anomalies    []fault.Anomaly
	MaxSkewTicks int64

	Segments      int
	LookupTotal   int
	LookupHits    int
	Partial       int
	Unrecoverable int
}

// Records maps every window onto audit segment records in stable order
// (event order, then layer order as supplied).
func (r *FanoutResult) Records() []audit.SegmentRecord {
	out := make([]audit.SegmentRecord, 0, len(r.Windows))
	for _, w := range r.Windows {
		out = append(out, w.Record())
	}
	return out
}

// Fanout plans and cuts every event against every layer independently, then
// fans the chosen boundaries back in on the shared clock to measure skew.
func Fanout(events []Event, layers []*Layer, opts Options) *FanoutResult {
	res := &FanoutResult{}
	byName := make(map[string]*Layer, len(layers))
	for _, l := range layers {
		byName[l.Name] = l
	}

	byEvent := make(map[string][]*Window)
	var eventOrder []string
	for _, ev := range events {
		if _, seen := byEvent[ev.ID]; !seen {
			eventOrder = append(eventOrder, ev.ID)
		}
		for _, l := range layers {
			w, err := PlanWindow(ev, l, opts)
			if err != nil {
				// A fatal mapping failure becomes an unrecoverable window so
				// the run can preserve the manifest and report exit 2/1.
				w = &Window{Event: ev, LayerName: l.Name, InputDigest: l.InputDigest}
				if fe, ok := fault.AsFatal(err); ok {
					w.Anomalies = append(w.Anomalies, *fatalAnomaly(fe, l))
				} else {
					w.Anomalies = append(w.Anomalies, fault.New(l.Name, 0, fault.CategorySegmentFail,
						fault.SeverityFatal, fault.ActionNone, 0, fault.Range{}, err.Error()))
				}
			}
			res.Windows = append(res.Windows, w)
			byEvent[ev.ID] = append(byEvent[ev.ID], w)
			res.Anomalies = append(res.Anomalies, w.Anomalies...)
			res.LookupTotal++
			if w.LookupHit {
				res.LookupHits++
			}
			if w.Recoverable {
				res.Segments++
			}
			if w.Partial {
				res.Partial++
			}
			if !w.Recoverable {
				res.Unrecoverable++
			}
		}
	}

	// Fan-in: cross-check each event's chosen start keyframes exactly.
	for _, id := range eventOrder {
		ws := byEvent[id]
		var ref *Window
		for _, w := range ws {
			if w.HasKeyframe {
				ref = w
				break
			}
		}
		if ref == nil {
			continue
		}
		rl := byName[ref.LayerName]
		refTS := int64(rl.Index.Track.Timescale)
		refFrame := rl.Clock.FrameTicks
		refDT := ref.Keyframe.DecodeTime
		for _, w := range ws {
			l := byName[w.LayerName]
			ts := int64(l.Index.Track.Timescale)
			b := audit.Boundary{
				EventID: id, Layer: w.LayerName, Edge: "start",
				TargetNS: w.StartNS, FrameTicks: l.Clock.FrameTicks,
			}
			if !w.HasKeyframe {
				b.WithinFrame = false
				res.Boundaries = append(res.Boundaries, b)
				continue
			}
			dt := w.Keyframe.DecodeTime
			// Exact cross-timescale comparison in reference ticks:
			//   skew = |dt*refTS - refDT*ts| / ts   (floor, ref ticks)
			num := abs64(dt*refTS - refDT*ts)
			skew := int64(0)
			if ts > 0 {
				skew = num / ts
			}
			w.SkewRefTicks = skew
			if skew > res.MaxSkewTicks {
				res.MaxSkewTicks = skew
			}
			exceeds := num > refFrame*ts
			b.ChosenNS = approxNS(l, dt)
			b.SkewTicks = skew
			b.WithinFrame = !exceeds
			res.Boundaries = append(res.Boundaries, b)
			if exceeds {
				w.Partial = true
				res.Partial++
				a := fault.New(w.LayerName, 0, fault.CategoryBoundarySkew, fault.SeverityError,
					fault.ActionMarkedPart, uint32(ts),
					fault.Range{Start: clock.Tick{Value: refDT}, End: clock.Tick{Value: dt}},
					fmt.Sprintf("cross-layer boundary skew %d ref-ticks exceeds one frame %d", skew, refFrame))
				w.Anomalies = append(w.Anomalies, a)
				res.Anomalies = append(res.Anomalies, a)
			}
		}
	}
	return res
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func approxNS(l *Layer, dt int64) int64 {
	if l == nil || l.Clock == nil {
		return 0
	}
	ns, _, err := l.Clock.ToNS(clock.Tick{Value: dt, RemDen: 1})
	if err != nil {
		return 0
	}
	return ns
}

func fatalAnomaly(fe *fault.FatalError, l *Layer) *fault.Anomaly {
	layer := fe.Layer
	if layer == "" && l != nil {
		layer = l.Name
	}
	a := fault.New(layer, fe.Offset, fault.CategorySegmentFail, fault.SeverityFatal,
		fault.ActionNone, 0, fe.Range, fe.Error())
	return &a
}

// RoundtripNS converts ns -> exact layer tick -> ns and reports whether the
// original timestamp and zero sub-nanosecond remainder were recovered.
func RoundtripNS(c *clock.Clock, ns int64) (back int64, remainder int64, exact bool, err error) {
	t, err := c.FromNS(ns)
	if err != nil {
		return 0, 0, false, err
	}
	back, remainder, err = c.ToNS(t)
	if err != nil {
		return 0, 0, false, err
	}
	return back, remainder, back == ns && remainder == 0, nil
}
