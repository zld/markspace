// Package meta parses track descriptions from the supported MP4 and MXF
// container subsets and merges rotation, crop and colour descriptors under
// explicit, documented priority rules. Every merge decision is recorded with
// its source so conflicting declarations are never silently overwritten.
package meta

import "fmt"

// Descriptor kinds and source containers.
const (
	KindMP4 = "mp4"
	KindMXF = "mxf"

	// PriorityMXF is the fixed source rank: MXF material-package descriptors
	// outrank MP4 track-level descriptors, which outrank container defaults.
	PriorityMXF     = 3
	PriorityMP4     = 2
	PriorityDefault = 1
)

// Rotation in integer degrees clockwise, normalised to {0,90,180,270}.
type Rotation struct {
	Degrees  int32
	Source   string
	Priority int
}

// Crop in integer pixels relative to the uncropped frame.
type Crop struct {
	X, Y, Width, Height int64
	Source              string
	Priority            int
}

// Colour is an integer colour description (nclx-style primaries/transfer/
// matrix plus a full-range flag). Zero-valued numeric fields mean "unset".
type Colour struct {
	Primaries uint16
	Transfer  uint16
	Matrix    uint16
	FullRange bool
	Source    string
	Priority  int
}

// Decision records one merge resolution, including conflicts where the
// incoming descriptor was rejected because a higher-priority value existed.
type Decision struct {
	Field  string `json:"field"`
	Action string `json:"action"` // "set", "kept", "conflict"
	Source string `json:"source"`
	Detail string `json:"detail"`
}

func (d Decision) String() string {
	return fmt.Sprintf("%s: %s via %s (%s)", d.Field, d.Action, d.Source, d.Detail)
}

// TrackDescription is everything one track contributes to the merge.
type TrackDescription struct {
	Container string // KindMP4 or KindMXF
	TrackID   uint32
	Timescale uint32 // MP4 mdhd timescale; 0 for MXF (uses edit-rate ticks)
	Width     uint32 // coded width/height in pixels
	Height    uint32
	Rotation  *Rotation
	Crop      *Crop
	Colour    *Colour
}

// Resolved is the merged, conflict-free description consumed downstream.
type Resolved struct {
	Rotation  Rotation
	Crop      Crop
	Colour    Colour
	Width     uint32
	Height    uint32
	Decisions []Decision

	// colPri stores the winning source priority per colour field.
	colPri map[string]int
}

// HasCrop reports whether any crop window was declared.
func (r *Resolved) HasCrop() bool { return r.Crop.Width > 0 && r.Crop.Height > 0 }

// HasColour reports whether any colour descriptor was declared.
func (r *Resolved) HasColour() bool {
	return r.Colour.Primaries != 0 || r.Colour.Transfer != 0 || r.Colour.Matrix != 0
}
