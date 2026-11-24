// Package scan implements phase one of the pipeline: container scanning.
// Scanners walk box/KLV boundaries segment by segment, record damage with
// byte offsets and error codes, and emit track/sample tables that the index
// and the streaming executor share. Scanners never modify the input.
package scan

import (
	"fmt"
	"os"

	"archive/internal/model"
)

// SampleRef locates one media packet inside the input file.
type SampleRef struct {
	Offset int64 `json:"offset"`
	Size   int64 `json:"size"`
	PTS    int64 `json:"pts"` // in the track's timescale
	Bad    bool  `json:"bad"` // failed integrity check; demux skips it
}

// ColorDesc is one colour description as found in the container. Conflicting
// descriptions are preserved verbatim, never rewritten.
type ColorDesc struct {
	Primaries uint16 `json:"primaries"`
	Transfer  uint16 `json:"transfer"`
	Matrix    uint16 `json:"matrix"`
	FullRange bool   `json:"fullRange"`
}

// Crop is a clean-aperture style crop in pixels.
type Crop struct {
	Top    int32 `json:"top"`
	Bottom int32 `json:"bottom"`
	Left   int32 `json:"left"`
	Right  int32 `json:"right"`
}

// TrackMeta describes one track without interpreting the media itself.
type TrackMeta struct {
	ID            int         `json:"id"`
	Type          string      `json:"type"` // "video" | "audio"
	Timescale     int64       `json:"timescale"`
	DurationUnits int64       `json:"durationUnits"`
	Rotation      int         `json:"rotation"` // degrees, from the track matrix
	HasRotation   bool        `json:"hasRotation"`
	Crop          *Crop       `json:"crop,omitempty"`
	Color         *ColorDesc  `json:"color,omitempty"`
	ColorAlt      *ColorDesc  `json:"colorAlt,omitempty"` // conflicting description, if any
	ColorConflict bool        `json:"colorConflict"`
	Samples       []SampleRef `json:"samples"`
}

// DurationNs is the track duration on the master timeline.
func (t *TrackMeta) DurationNs() int64 {
	return model.ToMaster(t.DurationUnits, model.Timescale(t.Timescale))
}

// Result is the output of a scan: tracks, time ranges, byte offsets and all
// defects found along the way.
type Result struct {
	Format     string       `json:"format"` // "mp4" | "mxf"
	Size       int64        `json:"size"`
	DurationNs int64        `json:"durationNs"`
	Tracks     []TrackMeta  `json:"tracks"`
	Partitions []int64      `json:"partitions,omitempty"` // MXF partition offsets
	Errors     model.Errors `json:"errors"`
}

// SampleCount counts intact (recoverable) samples across all tracks.
func (r *Result) SampleCount() (total, bad int) {
	for _, t := range r.Tracks {
		for _, s := range t.Samples {
			total++
			if s.Bad {
				bad++
			}
		}
	}
	return total, bad
}

// Scan sniffs the container format and dispatches to the right scanner.
func Scan(path string) (*Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var head [16]byte
	n, _ := f.ReadAt(head[:], 0)
	if n >= 8 && string(head[4:8]) == "ftyp" {
		return ScanMP4(path)
	}
	if n >= 4 && head[0] == 0x06 && head[1] == 0x0E && head[2] == 0x2B && head[3] == 0x34 {
		return ScanMXF(path)
	}
	return nil, fmt.Errorf("unrecognized container (no ftyp/KLV signature): %s", path)
}
