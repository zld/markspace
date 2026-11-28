package manifest

import (
	"fmt"
	"sort"

	"mediapipe/internal/model"
)

// Manifest is the top-level version manifest: one entry per asset, each with
// the versions that were published for it.
type Manifest struct {
	Seed   int64   `json:"seed"`
	Window int     `json:"window,omitempty"`
	Assets []Asset `json:"assets"`
}

// Asset groups all published versions of one media asset.
type Asset struct {
	Name     string    `json:"name"`
	Versions []Version `json:"versions"`
}

// Version is one published rendition (for example "v720p") of an asset.
// Priority is nil unless the manifest explicitly ranks versions; only an
// explicit priority may resolve a missing item or a summary conflict.
type Version struct {
	ID       string  `json:"id"`
	Priority *int    `json:"priority,omitempty"`
	Tracks   []Track `json:"tracks"`
}

// HasPriority reports whether the version was explicitly ranked.
func (v Version) HasPriority() bool { return v.Priority != nil }

// Track describes one media track of a version. Timescale and EditMediaTime
// are kept in raw media ticks so the original timestamps remain reproducible.
type Track struct {
	ID            string  `json:"id"`
	Kind          string  `json:"kind"`
	Timescale     uint32  `json:"timescale"`
	EditMediaTime int64   `json:"edit_media_time"`
	Ranges        []Range `json:"ranges"`
}

// Range is one published time interval of a track, expressed in media ticks.
// Summary identifies the encoded content of that interval so versions can be
// compared; samples and bytes bound the encoded payload.
type Range struct {
	StartTicks    int64  `json:"start_ticks"`
	DurationTicks int64  `json:"duration_ticks"`
	Summary       string `json:"summary"`
	// Chunks are per-grid-cell content digests covering the range. They let
	// versions that slice the same content differently be compared without
	// false conflicts.
	Chunks   []string `json:"chunks,omitempty"`
	Samples  int      `json:"samples,omitempty"`
	Bytes    int64    `json:"bytes,omitempty"`
	Sparse   bool     `json:"sparse,omitempty"`
	GopTicks int64    `json:"gop_ticks,omitempty"`
}

// EndTicks returns the exclusive end of the range in media ticks.
func (r Range) EndTicks() int64 { return r.StartTicks + r.DurationTicks }

// RawTime returns the range start as a raw timestamp that retains timescale
// and edit-list offset.
func (r Range) RawTime(t Track) (model.Time, error) {
	return model.NewTime(r.StartTicks, t.Timescale, t.EditMediaTime)
}

// Span maps the range onto the presentation timeline in nanoseconds.
func (r Range) Span(t Track) (model.Span, error) {
	start, err := r.RawTime(t)
	if err != nil {
		return model.Span{}, err
	}
	endTime, err := model.NewTime(r.EndTicks(), t.Timescale, t.EditMediaTime)
	if err != nil {
		return model.Span{}, err
	}
	a, err := start.ShiftedNanos()
	if err != nil {
		return model.Span{}, err
	}
	b, err := endTime.ShiftedNanos()
	if err != nil {
		return model.Span{}, err
	}
	return model.NewSpan(a, b)
}

// TrackByID finds a track inside the version.
func (v Version) TrackByID(id string) (Track, bool) {
	for _, t := range v.Tracks {
		if t.ID == id {
			return t, true
		}
	}
	return Track{}, false
}

// SortedVersions returns versions ordered by explicit priority (highest
// first) and then by ID, giving deterministic tie-breaking only where the
// manifest actually ranked the versions.
func (a Asset) SortedVersions() []Version {
	out := append([]Version(nil), a.Versions...)
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := out[i].HasPriority(), out[j].HasPriority()
		switch {
		case pi && pj && *out[i].Priority != *out[j].Priority:
			return *out[i].Priority > *out[j].Priority
		case pi != pj:
			return pi
		default:
			return out[i].ID < out[j].ID
		}
	})
	return out
}

// VersionByID finds a version by identifier.
func (a Asset) VersionByID(id string) (Version, bool) {
	for _, v := range a.Versions {
		if v.ID == id {
			return v, true
		}
	}
	return Version{}, false
}

func (t Track) String() string { return t.Kind + "/" + t.ID }

func (r Range) String() string {
	return fmt.Sprintf("%s[%d,+%d]", r.Summary, r.StartTicks, r.DurationTicks)
}
