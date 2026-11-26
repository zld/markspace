package process

import (
	"fmt"
	"sort"

	"mediaprep/internal/model"
	"mediaprep/internal/source"
)

// DeliveryClock is the shared integer-tick clock used in terminal results.
// 48 kHz exactly represents both the generated 12 kHz MP4 media clock (x4)
// and 48 kHz PCM.
var DeliveryClock = model.NewClock(1, 48000)

type CutRequest struct {
	Clock    model.Clock `json:"clock"`
	CutTicks []int64     `json:"cut_ticks"`
}

type Snap struct {
	RequestedTicks int64       `json:"requested_ticks"`
	ActualTicks    int64       `json:"actual_ticks"`
	DeviationTicks int64       `json:"deviation_ticks"`
	Clock          model.Clock `json:"clock"`
	RequestedMS    float64     `json:"requested_ms"`
	ActualMS       float64     `json:"actual_ms"`
}

type Segment struct {
	Index       int            `json:"index"`
	ID          string         `json:"id"`
	Track       string         `json:"track"`
	Role        string         `json:"role"`
	Interval    model.Interval `json:"interval"`
	TrackStart  int64          `json:"track_start"`
	TrackEnd    int64          `json:"track_end"`
	FirstSample int            `json:"first_sample"`
	EndSample   int            `json:"end_sample"`
	ByteSize    int            `json:"byte_size"`
	SnapToNext  bool           `json:"snap_to_next_sample"`
}

type CutResult struct {
	Segments []Segment `json:"segments"`
	Snaps    []Snap    `json:"snaps,omitempty"`
}

// PlanCuts maps rational shared-clock cut points to sample boundaries. A point
// strictly inside/between samples is moved forward to the next sample start.
func PlanCuts(track *source.Track, req CutRequest) (*CutResult, error) {
	if len(track.Samples) == 0 {
		return nil, model.NewError(model.CodeInvalidMetadata, model.PhaseProcess, track.Label, -1,
			"cannot cut track without samples")
	}
	bounds := make([]int64, 0, len(track.Samples)+1)
	for _, s := range track.Samples {
		bounds = append(bounds, s.DTS)
	}
	last := track.Samples[len(track.Samples)-1]
	bounds = append(bounds, last.DTS+last.Duration)

	boundaryIndexes := []int{0}
	res := &CutResult{}
	for _, sharedAt := range req.CutTicks {
		target := model.ConvertTicks(sharedAt, req.Clock, track.Clock)
		j := sort.Search(len(bounds), func(i int) bool { return bounds[i] >= target })
		if j == len(bounds) {
			j = len(bounds) - 1
		}
		actual := bounds[j]
		if actual > target {
			res.Snaps = append(res.Snaps, Snap{
				RequestedTicks: target, ActualTicks: actual, DeviationTicks: actual - target,
				Clock:       track.Clock,
				RequestedMS: ticksToMS(target, track.Clock),
				ActualMS:    ticksToMS(actual, track.Clock),
			})
		}
		boundaryIndexes = append(boundaryIndexes, j)
	}
	boundaryIndexes = append(boundaryIndexes, len(bounds)-1)
	sort.Ints(boundaryIndexes)
	boundaryIndexes = compactInts(boundaryIndexes)

	for i := 0; i+1 < len(boundaryIndexes); i++ {
		a, b := boundaryIndexes[i], boundaryIndexes[i+1]
		if a == b {
			continue
		}
		firstSample := a
		endSample := b
		byteSize := 0
		for si := firstSample; si < endSample && si < len(track.Samples); si++ {
			byteSize += track.Samples[si].Size
		}
		startShared := model.ConvertTicks(bounds[a], track.Clock, DeliveryClock)
		endShared := model.ConvertTicks(bounds[b], track.Clock, DeliveryClock)
		res.Segments = append(res.Segments, Segment{
			Index: i, ID: fmt.Sprintf("%s-seg-%d", track.Label, i+1), Track: track.Label,
			Role:       track.Role,
			Interval:   model.Interval{Start: startShared, End: endShared, Scale: DeliveryClock.TickDuration},
			TrackStart: bounds[a], TrackEnd: bounds[b], FirstSample: firstSample, EndSample: endSample,
			ByteSize: byteSize, SnapToNext: hasSnapAround(res.Snaps, bounds[a]),
		})
	}
	// Reindex after compacting endpoint-only cuts.
	for i := range res.Segments {
		res.Segments[i].Index = i
		res.Segments[i].ID = fmt.Sprintf("%s-seg-%d", track.Label, i+1)
	}
	return res, nil
}

func ticksToMS(ticks int64, c model.Clock) float64 {
	// tick duration numerator/denominator is seconds/tick.
	return float64(ticks) * float64(c.TickDuration.Numerator) / float64(c.TickDuration.Denominator) * 1000
}

func compactInts(in []int) []int {
	out := in[:0]
	for _, v := range in {
		if len(out) == 0 || out[len(out)-1] != v {
			out = append(out, v)
		}
	}
	return out
}

func hasSnapAround(snaps []Snap, boundary int64) bool {
	for _, s := range snaps {
		if s.ActualTicks == boundary {
			return true
		}
	}
	return false
}
