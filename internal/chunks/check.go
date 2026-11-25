// Package chunks validates monotonic, non-overlapping fragment boundaries and
// coverage of an event target interval.
package chunks

import (
	"fmt"

	"replaycheck/internal/clock"
)

// Chunk is one media fragment boundary on the shared nanosecond basis.
type Chunk struct {
	Sequence  int   `json:"sequence"`
	StartNS   int64 `json:"start_ns"`
	EndNS     int64 `json:"end_ns"`
	Offset    int64 `json:"byte_offset"`
	EndOffset int64 `json:"end_byte_offset,omitempty"`
	Complete  bool  `json:"complete"`
}

// Validate checks ordering, overlap/gaps, and target coverage. Every returned
// error carries the chunk sequence, expected boundary, and actual deviation.
func Validate(list []Chunk, targetStartNS, targetEndNS int64) []*clock.Error {
	errs := []*clock.Error{}
	if targetEndNS < targetStartNS {
		errs = append(errs, clock.NewError(clock.CodeChunkCoverage,
			"target interval ends before it starts", -1).
			WithChunk(-1, targetStartNS, targetEndNS))
		return errs
	}
	if len(list) == 0 {
		errs = append(errs, clock.NewError(clock.CodeChunkCoverage,
			"no chunks cover target interval", -1).
			WithChunk(0, targetStartNS, 0))
		return errs
	}

	first := list[0]
	if first.StartNS > targetStartNS {
		errs = append(errs, clock.NewError(clock.CodeChunkCoverage,
			fmt.Sprintf("chunk %d starts after target start", first.Sequence), first.Offset).
			WithChunk(first.Sequence, targetStartNS, first.StartNS))
	}
	if first.StartNS > first.EndNS {
		errs = append(errs, clock.NewError(clock.CodeChunkOrder,
			fmt.Sprintf("chunk %d end precedes start", first.Sequence), first.Offset).
			WithChunk(first.Sequence, first.StartNS, first.EndNS))
	}

	var prev Chunk
	for i, c := range list {
		if c.Sequence == 0 {
			c.Sequence = i + 1
		}
		if c.EndNS < c.StartNS {
			errs = append(errs, clock.NewError(clock.CodeChunkOrder,
				fmt.Sprintf("chunk %d end precedes start", c.Sequence), c.Offset).
				WithChunk(c.Sequence, c.StartNS, c.EndNS))
		}
		if i == 0 {
			prev = c
			continue
		}

		if c.StartNS < prev.StartNS || c.EndNS < prev.EndNS {
			errs = append(errs, clock.NewError(clock.CodeChunkOrder,
				fmt.Sprintf("chunk %d boundaries are not monotonic after chunk %d", c.Sequence, prev.Sequence), c.Offset).
				WithChunk(c.Sequence, prev.EndNS, c.StartNS))
		}

		switch {
		case c.StartNS < prev.EndNS:
			errs = append(errs, clock.NewError(clock.CodeChunkOverlap,
				fmt.Sprintf("chunk %d overlaps chunk %d", c.Sequence, prev.Sequence), c.Offset).
				WithChunk(c.Sequence, prev.EndNS, c.StartNS))
		case c.StartNS > prev.EndNS:
			errs = append(errs, clock.NewError(clock.CodeChunkCoverage,
				fmt.Sprintf("gap between chunk %d and chunk %d", prev.Sequence, c.Sequence), c.Offset).
				WithChunk(c.Sequence, prev.EndNS, c.StartNS))
		}
		prev = c
	}

	last := list[len(list)-1]
	if last.EndNS < targetEndNS {
		errs = append(errs, clock.NewError(clock.CodeChunkCoverage,
			fmt.Sprintf("chunk %d ends before target end", last.Sequence), last.EndOffset).
			WithChunk(last.Sequence, targetEndNS, last.EndNS))
	}
	return errs
}

// Anomalies converts validation errors to indexable anomalies.
func Anomalies(errs []*clock.Error, path string) []clock.Anomaly {
	out := make([]clock.Anomaly, 0, len(errs))
	for _, err := range errs {
		err.Path = path
		a := clock.AnomalyFromError(err, "error")
		out = append(out, a)
	}
	return out
}

// PlanContiguous builds equal-duration contiguous chunks over a frame-aligned
// interval. The final chunk absorbs integer-nanosecond rounding.
func PlanContiguous(sequenceBase int, startNS, endNS int64, count int, byteOffsets []int64) []Chunk {
	if count <= 0 || endNS <= startNS {
		return nil
	}
	span := endNS - startNS
	step := span / int64(count)
	out := make([]Chunk, 0, count)
	for i := 0; i < count; i++ {
		s := startNS + int64(i)*step
		e := s + step
		if i == count-1 {
			e = endNS
		}
		off := int64(-1)
		if i < len(byteOffsets) {
			off = byteOffsets[i]
		}
		out = append(out, Chunk{Sequence: sequenceBase + i + 1, StartNS: s, EndNS: e, Offset: off, Complete: true})
	}
	return out
}
