package media

import (
	"errors"
	"fmt"
)

// ColorMetadata is the normalized color description exchanged with MP4 colr
// boxes. Constants follow ISO/IEC 23001-8 / ITU-T H.273 code points.
type ColorMetadata struct {
	Primaries     uint16
	Transfer      uint16
	Matrix        uint16
	FullRange     bool
	SourceTrackID string // provenance, kept for conflict diagnostics
}

// Standard code points used by the fixture generator and policy defaults.
const (
	CPBT709  uint16 = 1 // HD/sRGB primaries and transfer
	CPUnspec uint16 = 2
	CPBT601  uint16 = 6 // 525-line SD
	CPBT2020 uint16 = 9 // HDR wide gamut
)

// ProjectPolicy supplies inherited defaults when a source omits color tags.
type ProjectPolicy struct {
	Default ColorMetadata
	// AllowInheritance permits missing source metadata to take project values.
	AllowInheritance bool
}

// ColorConflict lists every track whose metadata disagrees, preserving the
// original source values instead of silently overwriting them.
type ColorConflict struct {
	TrackID  string
	Existing ColorMetadata
	Incoming ColorMetadata
	Field    string
}

func (c ColorConflict) Error() string {
	return fmt.Sprintf("color conflict on %s: track %s disagrees", c.Field, c.TrackID)
}

// ErrColorConflict marks an unresolvable metadata disagreement.
var ErrColorConflict = errors.New("color metadata conflict")

// ColorTable is the running normalized view of all tracks in a package.
type ColorTable struct {
	policy   ProjectPolicy
	byTrack  map[string]ColorMetadata
	conflict *ColorConflict
}

// NewColorTable binds a project policy.
func NewColorTable(p ProjectPolicy) *ColorTable {
	return &ColorTable{policy: p, byTrack: map[string]ColorMetadata{}}
}

// Ingest registers one source description. Missing fields inherit the project
// policy when allowed; conflicting explicit values block the table and retain
// both provenance records.
func (t *ColorTable) Ingest(m ColorMetadata) error {
	filled := m
	inherited := false
	if filled.Primaries == 0 {
		filled.Primaries = t.policy.Default.Primaries
		inherited = true
	}
	if filled.Transfer == 0 {
		filled.Transfer = t.policy.Default.Transfer
		inherited = true
	}
	if filled.Matrix == 0 {
		filled.Matrix = t.policy.Default.Matrix
		inherited = true
	}
	if inherited && !t.policy.AllowInheritance && m.SourceTrackID != "" {
		return fmt.Errorf("%w: track %s missing color tags and policy forbids inheritance",
			ErrColorConflict, m.SourceTrackID)
	}
	// Compare against every already-accepted explicit (non-inherited) source.
	for id, prev := range t.byTrack {
		if c := compareColor(prev, filled); c != nil {
			c.TrackID = m.SourceTrackID
			if c.TrackID == "" {
				c.TrackID = id
			}
			t.conflict = c
			return fmt.Errorf("%w: %w", ErrColorConflict, c)
		}
	}
	t.byTrack[m.SourceTrackID] = filled
	return nil
}

func compareColor(a, b ColorMetadata) *ColorConflict {
	switch {
	case a.Primaries != b.Primaries:
		return &ColorConflict{Existing: a, Incoming: b, Field: "primaries"}
	case a.Transfer != b.Transfer:
		return &ColorConflict{Existing: a, Incoming: b, Field: "transfer"}
	case a.Matrix != b.Matrix:
		return &ColorConflict{Existing: a, Incoming: b, Field: "matrix"}
	case a.FullRange != b.FullRange:
		return &ColorConflict{Existing: a, Incoming: b, Field: "full_range"}
	}
	return nil
}

// Snapshot returns the normalized per-track metadata.
func (t *ColorTable) Snapshot() map[string]ColorMetadata {
	out := make(map[string]ColorMetadata, len(t.byTrack))
	for k, v := range t.byTrack {
		out[k] = v
	}
	return out
}

// Blocked reports whether a conflict prevents commit of the package.
func (t *ColorTable) Blocked() bool { return t.conflict != nil }

// Conflict returns the retained provenance records for diagnostics.
func (t *ColorTable) Conflict() *ColorConflict { return t.conflict }
