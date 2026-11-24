// Package probe extracts raw track metadata from MP4 and MXF containers.
// It deliberately preserves every rotation, crop and colour declaration,
// including conflicting ones, so that normalisation can resolve them with
// explicit rules instead of silently overwriting them here.
package probe

import (
	"fmt"
	"time"
)

// Declaration sources are recorded with every value so downstream conflict
// reports can name where a claim came from.
const (
	SourceTkhd    = "mp4.tkhd.matrix"
	SourceClap    = "mp4.stsd.clap"
	SourceColr    = "mp4.stsd.colr"
	SourceMXFRot  = "mxf.video.rotation"
	SourceMXFCrop = "mxf.video.crop"
	SourceMXFCol  = "mxf.video.color"
)

// RotationDecl is one explicit rotation claim, in degrees clockwise.
type RotationDecl struct {
	Degrees int
	Source  string
}

// CropDecl is one explicit crop rectangle in stored-frame pixels.
type CropDecl struct {
	Left, Right, Top, Bottom int
	Source                   string
}

// Width and Height return the cropped frame size.
func (c CropDecl) Width() int  { return c.Right - c.Left }
func (c CropDecl) Height() int { return c.Bottom - c.Top }

// Empty reports whether the crop covers nothing.
func (c CropDecl) Empty() bool { return c.Width() <= 0 || c.Height() <= 0 }

// ColorDecl is one colour-description claim. Values follow H.273 enums.
type ColorDecl struct {
	Primaries uint16
	Transfer  uint16
	Matrix    uint16
	FullRange bool
	Source    string
}

// Equal reports whether two declarations describe the same colour space.
func (c ColorDecl) Equal(o ColorDecl) bool {
	return c.Primaries == o.Primaries && c.Transfer == o.Transfer &&
		c.Matrix == o.Matrix && c.FullRange == o.FullRange
}

// Track is container-independent raw metadata for one media track.
type Track struct {
	ID     string
	Format string // "mp4" or "mxf"
	Kind   string // "video" or "audio"
	Index  int
	Offset uint64 // byte offset of the track header, for error reporting

	StoredWidth   int
	StoredHeight  int
	DisplayWidth  int
	DisplayHeight int

	Timescale        uint32
	DurationUnits    int64
	DurationNS       int64
	EditStartNS      int64
	SampleIntervalNS int64
	SampleCount      int

	Rotations []RotationDecl
	Crops     []CropDecl
	Colors    []ColorDecl
	Warnings  []string
}

// Container is a parsed media file.
type Container struct {
	Format     string
	Timescale  uint32
	DurationNS int64
	Tracks     []*Track
	Warnings   []string
}

// FrameInterval returns the track's nominal frame spacing.
func (t *Track) FrameInterval() time.Duration {
	return time.Duration(t.SampleIntervalNS)
}

// AddWarning records a non-fatal observation.
func (t *Track) AddWarning(format string, args ...interface{}) {
	t.Warnings = append(t.Warnings, fmt.Sprintf(format, args...))
}
