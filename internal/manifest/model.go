package manifest

import (
	"time"

	"continuity/internal/media"
)

// Segment is one normalized local media reference.
type Segment struct {
	Index         int    `json:"index"`
	Track         string `json:"track"`
	Kind          string `json:"kind"`
	Sequence      uint64 `json:"sequence"`
	URI           string `json:"uri"`
	RelPath       string `json:"rel_path"`
	AbsPath       string `json:"abs_path"`
	InitRelPath   string `json:"init_rel_path,omitempty"`
	InitAbsPath   string `json:"init_abs_path,omitempty"`
	StartNS       int64  `json:"start_ns"`
	DurationNS    int64  `json:"duration_ns"`
	Discontinuity bool   `json:"discontinuity"`
	Source        string `json:"source"`
	Line          int    `json:"line,omitempty"`
}

// Manifest is the format-neutral normalized playlist/MPD.
type Manifest struct {
	Format        string    `json:"format"`
	Version       int       `json:"version"`
	Source        string    `json:"source"`
	Root          string    `json:"root"`
	MediaSequence uint64    `json:"media_sequence"`
	Static        bool      `json:"static"`
	Timescale     uint32    `json:"timescale,omitempty"`
	Segments      []Segment `json:"segments"`
	ParsedAt      time.Time `json:"-"`
}

// Limits bounds manifest reads and reference counts.
type Limits struct {
	MaxBytes    int64
	MaxSegments int
}

func DefaultLimits() Limits {
	return Limits{MaxBytes: media.MaxInputBytes, MaxSegments: 4096}
}

// Bundle collects the audio and video manifests resolved from one entry.
type Bundle struct {
	Entry     string      `json:"entry"`
	Root      string      `json:"root"`
	Manifests []*Manifest `json:"manifests"`
	Segments  []Segment   `json:"segments"`
}
