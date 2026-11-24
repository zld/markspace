package pipeline

import (
	"encoding/json"
	"io"
	"os"

	"archive/internal/clock"
	"archive/internal/normalize"
	"archive/internal/probe"
)

// ProbeReport is the metadata view printed by `archive probe`.
type ProbeReport struct {
	File       string       `json:"file"`
	Format     string       `json:"format"`
	Timescale  uint32       `json:"timescale"`
	DurationNS int64        `json:"duration_ns"`
	Tracks     []ProbeTrack `json:"tracks"`
	Warnings   []string     `json:"warnings,omitempty"`
}

// ProbeTrack is one track's preserved claims and resolved transform.
type ProbeTrack struct {
	ID               string   `json:"id"`
	Kind             string   `json:"kind"`
	StoredWidth      int      `json:"stored_width"`
	StoredHeight     int      `json:"stored_height"`
	Timescale        uint32   `json:"timescale"`
	SampleIntervalNS int64    `json:"sample_interval_ns"`
	SampleCount      int      `json:"sample_count"`
	DurationNS       int64    `json:"duration_ns"`
	Rotations        []int    `json:"rotation_declarations"`
	Crops            [][4]int `json:"crop_declarations"`
	ColorSources     []string `json:"color_sources"`
	CropRule         string   `json:"crop_rule"`
	CropSource       string   `json:"crop_source"`
	Rotation         int      `json:"rotation"`
	ColorResolved    bool     `json:"color_resolved"`
	ColorConflict    bool     `json:"color_conflict"`
	Warnings         []string `json:"warnings,omitempty"`
}

// ProbeFile parses and normalises one container for inspection.
func ProbeFile(path string) (*ProbeReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, clock.New(clock.KindInvalidMetadata, "read file").WithLocation(path).WithCause(err)
	}
	c, err := probe.Sniff(data)
	if err != nil {
		return nil, err
	}
	rep := &ProbeReport{File: path, Format: c.Format, Timescale: c.Timescale, DurationNS: c.DurationNS}
	rep.Warnings = append(rep.Warnings, c.Warnings...)
	for _, t := range c.Tracks {
		res, err := normalize.Resolve(t)
		if err != nil {
			return nil, err
		}
		pt := ProbeTrack{
			ID: t.ID, Kind: t.Kind,
			StoredWidth: t.StoredWidth, StoredHeight: t.StoredHeight,
			Timescale: t.Timescale, SampleIntervalNS: t.SampleIntervalNS,
			SampleCount: t.SampleCount, DurationNS: t.DurationNS,
			ColorSources: res.ColorSources,
			CropRule:     string(res.CropRule), CropSource: res.CropSource,
			Rotation: res.Rotation, ColorResolved: res.ColorResolved,
			ColorConflict: res.ColorConflict,
			Warnings:      res.Warnings,
		}
		for _, r := range t.Rotations {
			pt.Rotations = append(pt.Rotations, r.Degrees)
		}
		for _, cr := range t.Crops {
			pt.Crops = append(pt.Crops, [4]int{cr.Left, cr.Right, cr.Top, cr.Bottom})
		}
		rep.Tracks = append(rep.Tracks, pt)
	}
	return rep, nil
}

// PrintProbe writes the probe report as indented JSON.
func PrintProbe(w io.Writer, rep *ProbeReport) error {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}
