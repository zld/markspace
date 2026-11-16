// Package demo builds the built-in sample plan: three representations with
// different timescales and per-segment tick offsets, six segments each. The
// numbers are chosen so cross-rendition boundaries differ by known amounts
// (max start offset 12.5ms, max duration delta 1ms) for deterministic demos.
package demo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"dashgen/internal/plan"
)

// PlanFileName is the file written by WritePlan inside the output directory.
const PlanFileName = "plan.json"

type renditionSpec struct {
	id            string
	bandwidth     int64
	timescale     int64
	startTicks    int64
	startPerIndex []int64 // native-tick start of segment numbers 1..6
	durPerIndex   []int64 // native-tick duration of segment numbers 1..6
}

// r360: 300 tps, neat 100-tick (~333.3ms) segments starting at zero.
// r720: 90000 tps, same wall-clock starts but encoded on non-aligned ticks.
// r1080: 24000 tps, shifted pipeline origin, up to 12.5ms start offset.
func specs() []renditionSpec {
	return []renditionSpec{
		{
			id: "r360", bandwidth: 800000, timescale: 300, startTicks: 0,
			startPerIndex: []int64{0, 100, 200, 300, 400, 500},
			durPerIndex:   []int64{100, 100, 100, 100, 100, 100},
		},
		{
			id: "r720", bandwidth: 2500000, timescale: 90000, startTicks: 75,
			startPerIndex: []int64{75, 30075, 60075, 90075, 120075, 150075},
			durPerIndex:   []int64{30000, 30000, 30000, 30000, 30000, 30000},
		},
		{
			id: "r1080", bandwidth: 6000000, timescale: 24000, startTicks: 300,
			// Segment 1 is exactly 1ms shorter (7976 ticks = 332.333...ms);
			// subsequent starts shift by the same 24 ticks, staying gap-free.
			startPerIndex: []int64{300, 8276, 16276, 24276, 32276, 40276},
			durPerIndex:   []int64{7976, 8000, 8000, 8000, 8000, 8000},
		},
	}
}

// Build returns the in-memory sample plan.
func Build() *plan.Plan {
	specs := specs()
	reps := make([]plan.Representation, 0, len(specs))
	for _, s := range specs {
		segs := make([]plan.Segment, 0, len(s.startPerIndex))
		for i, start := range s.startPerIndex {
			segs = append(segs, plan.Segment{
				Number:        int64(i + 1),
				StartTicks:    start,
				DurationTicks: s.durPerIndex[i],
			})
		}
		reps = append(reps, plan.Representation{
			ID:           s.id,
			BandwidthBPS: s.bandwidth,
			Timescale:    s.timescale,
			StartTicks:   s.startTicks,
			Segments:     segs,
		})
	}
	return &plan.Plan{Representations: reps}
}

// WritePlan creates dir if needed and writes the sample plan as
// <dir>/plan.json with stable indentation.
func WritePlan(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("demo: create output dir: %w", err)
	}
	data, err := json.MarshalIndent(Build(), "", "  ")
	if err != nil {
		return "", fmt.Errorf("demo: marshal plan: %w", err)
	}
	data = append(data, '\n')
	path := filepath.Join(dir, PlanFileName)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("demo: write plan: %w", err)
	}
	return path, nil
}
