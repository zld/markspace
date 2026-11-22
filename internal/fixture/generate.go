// Package fixture produces deterministic encoding-rend descriptions
// without touching any external media. The generated metadata stands in
// for real multi-bitrate encoder outputs for demos and acceptance runs.
package fixture

import (
	"encoding/json"
	"os"
	"path/filepath"

	"dashcheck/internal/model"
)

// InputName is the fixed description file name inside a demo directory.
const InputName = "representations.json"

const timescale = int64(1000)

// DemoDescription builds the happy-path sample: two video rends, four
// segments each, timescale 1000. The first three boundaries are exactly
// 0/2000/4000 ticks on both rends; the tail segment starts differently
// (5200 vs 5000) but every rend still covers 6000 ticks in total.
func DemoDescription() *model.Description {
	return &model.Description{
		ContentID: "demo-cut-0001",
		Representations: []model.RepresentationInput{
			{
				ID: "video-hd", Bandwidth: 3_000_000, Width: 1920, Height: 1080,
				Timescale: timescale,
				Segments: []model.SegmentRef{
					{Number: 1, StartTick: 0, Duration: 2000, RandomAccess: true},
					{Number: 2, StartTick: 2000, Duration: 2000, RandomAccess: true},
					{Number: 3, StartTick: 4000, Duration: 1200, RandomAccess: true},
					{Number: 4, StartTick: 5200, Duration: 800, RandomAccess: true},
				},
			},
			{
				ID: "video-sd", Bandwidth: 800_000, Width: 854, Height: 480,
				Timescale: timescale,
				Segments: []model.SegmentRef{
					{Number: 1, StartTick: 0, Duration: 2000, RandomAccess: true},
					{Number: 2, StartTick: 2000, Duration: 2000, RandomAccess: true},
					{Number: 3, StartTick: 4000, Duration: 1000, RandomAccess: true},
					{Number: 4, StartTick: 5000, Duration: 1000, RandomAccess: true},
				},
			},
		},
	}
}

// SkewedDescription returns the failure-case sample: the low-bitrate
// rend's segment 3 starts 200 ticks (200 ms) late at 4200 and every
// later boundary/end shifts by the same amount. Original ticks are kept
// verbatim; nothing re-bases them onto the high-bitrate timeline.
func SkewedDescription() *model.Description {
	d := DemoDescription()
	for i := range d.Representations {
		if d.Representations[i].ID != "video-sd" {
			continue
		}
		d.Representations[i].Segments = []model.SegmentRef{
			{Number: 1, StartTick: 0, Duration: 2000, RandomAccess: true},
			{Number: 2, StartTick: 2000, Duration: 2000, RandomAccess: true},
			{Number: 3, StartTick: 4200, Duration: 2000, RandomAccess: true},
			{Number: 4, StartTick: 6200, Duration: 1000, RandomAccess: true},
		}
	}
	return d
}

// WriteDemo materializes the demo description as representations.json in
// dir. It never overwrites an existing description and never points at
// an MPD path.
func WriteDemo(dir string) (string, error) {
	return writeDescription(dir, DemoDescription())
}

// WriteSkewed materializes the failure-case description the same way.
func WriteSkewed(dir string) (string, error) {
	return writeDescription(dir, SkewedDescription())
}

func writeDescription(dir string, d *model.Description) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", model.Errorf(model.CategoryIO, model.CodeWriteFailed,
			"cannot create demo directory %q: %v", dir, err)
	}
	path := filepath.Join(dir, InputName)
	if _, err := os.Stat(path); err == nil {
		return "", model.Errorf(model.CategoryIO, model.CodePathConflict,
			"refusing to overwrite existing file %q", path)
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return "", model.Errorf(model.CategoryIO, model.CodeWriteFailed,
			"cannot encode demo description: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", model.Errorf(model.CategoryIO, model.CodeWriteFailed,
			"cannot write demo description %q: %v", path, err)
	}
	return path, nil
}
