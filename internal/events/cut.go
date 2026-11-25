// Package events maps replay event timestamps onto decoded frame boundaries.
package events

import (
	"fmt"

	"replaycheck/internal/clock"
)

// Event is a target interval on the shared nanosecond timeline.
type Event struct {
	ID      string `json:"id"`
	StartNS int64  `json:"start_ns"`
	EndNS   int64  `json:"end_ns"`
}

// Cut is the frame-aligned result for an event interval.
type Cut struct {
	Event
	TrackID             uint32 `json:"track_id"`
	StartFrame          int    `json:"start_frame"`
	EndFrame            int    `json:"end_frame"`
	CutStartNS          int64  `json:"cut_start_ns"`
	CutEndNS            int64  `json:"cut_end_ns"`
	StartQuantizationNS int64  `json:"start_quantization_ns"`
	EndQuantizationNS   int64  `json:"end_quantization_ns"`
	BoundaryErrorNS     int64  `json:"boundary_error_ns"`
}

// MapEvent maps both event boundaries to the first decodable frame at or
// after each requested time. It never uses an average frame rate.
func MapEvent(ev Event, timeline clock.Timeline) (Cut, error) {
	if ev.EndNS < ev.StartNS {
		return Cut{}, clock.NewError(clock.CodeEventOutOfRange,
			fmt.Sprintf("event %q has end before start", ev.ID), -1).
			WithInterval(ev.StartNS, ev.EndNS)
	}

	startFrame, startIndex, startQuant, ok := timeline.Boundary(ev.StartNS)
	if !ok {
		return Cut{}, clock.NewError(clock.CodeEventOutOfRange,
			fmt.Sprintf("event %q start %d is after final frame", ev.ID, ev.StartNS), -1).
			WithInterval(ev.StartNS, ev.EndNS)
	}
	endAt := ev.EndNS
	if endAt < ev.StartNS {
		endAt = ev.StartNS
	}
	endFrame, endIndex, endQuant, ok := timeline.Boundary(endAt)
	if !ok {
		// An inclusive endpoint exactly at or after the last frame start can
		// still use that last frame's end. Requesting beyond its end fails.
		last := timeline.Frames[len(timeline.Frames)-1]
		if endAt <= last.DTSNS+last.DurationNS {
			endFrame, endIndex = last, last.Index
			endQuant = 0
		} else {
			return Cut{}, clock.NewError(clock.CodeEventOutOfRange,
				fmt.Sprintf("event %q end %d is outside timeline", ev.ID, ev.EndNS), -1).
				WithInterval(ev.StartNS, ev.EndNS)
		}
	}

	errNS := startQuant
	if endQuant > errNS {
		errNS = endQuant
	}
	return Cut{
		Event: ev, TrackID: timeline.TrackID,
		StartFrame: startIndex, EndFrame: endIndex,
		CutStartNS: startFrame.DTSNS, CutEndNS: endFrame.DTSNS + endFrame.DurationNS,
		StartQuantizationNS: startQuant, EndQuantizationNS: endQuant,
		BoundaryErrorNS: errNS,
	}, nil
}

// MapEvents maps all events and returns cuts in input order.
func MapEvents(events []Event, timeline clock.Timeline) ([]Cut, []clock.Anomaly) {
	cuts := make([]Cut, 0, len(events))
	anomalies := []clock.Anomaly{}
	for _, ev := range events {
		cut, err := MapEvent(ev, timeline)
		if err != nil {
			ce := clock.AsError(err)
			anomalies = append(anomalies, clock.AnomalyFromError(ce, "error"))
			continue
		}
		cuts = append(cuts, cut)
	}
	return cuts, anomalies
}

// MaxQuantizationErrorNS returns the largest reported frame quantization gap.
func MaxQuantizationErrorNS(cuts []Cut) int64 {
	var max int64
	for _, c := range cuts {
		if c.BoundaryErrorNS > max {
			max = c.BoundaryErrorNS
		}
	}
	return max
}
