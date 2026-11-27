// Package manifest reads versioned archive manifests and normalises every
// timestamp onto the shared nanosecond clock.
package manifest

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
)

// Version identifies a manifest schema.
type Version int

const (
	// VersionUnknown is never accepted.
	VersionUnknown Version = 0
	// VersionLegacy is the v1 schema: times are integer milliseconds.
	VersionLegacy Version = 1
	// VersionCurrent is the v2 schema: times are integer nanoseconds and the
	// document carries an explicit track index.
	VersionCurrent Version = 2
)

// SupportedVersions lists every schema this build can read.
func SupportedVersions() []Version { return []Version{VersionLegacy, VersionCurrent} }

// Item is a normalised manifest entry. Times are nanoseconds on the shared
// clock and Track is the time base they were interpreted in.
type Item struct {
	ID         string     `json:"id"`
	Codec      string     `json:"codec"`
	Path       string     `json:"path,omitempty"`
	Track      string     `json:"track"`
	PTS        clock.Time `json:"pts_ns"`
	Duration   clock.Time `json:"duration_ns"`
	SourcePTS  string     `json:"source_pts"`
	SourceUnit string     `json:"source_unit"`
}

// Manifest is a normalised manifest independent of its source schema.
type Manifest struct {
	SourceVersion Version `json:"source_version"`
	Items         []Item  `json:"items"`
	Digest        string  `json:"digest,omitempty"`
}

// Duration returns the span from the earliest to the latest item end.
func (m *Manifest) Duration() clock.Time {
	if len(m.Items) == 0 {
		return 0
	}
	start := m.Items[0].PTS
	end := m.Items[0].PTS + m.Items[0].Duration
	for _, it := range m.Items[1:] {
		if it.PTS < start {
			start = it.PTS
		}
		if e := it.PTS + it.Duration; e > end {
			end = e
		}
	}
	return end - start
}

// TrackDuration returns the span of one track.
func (m *Manifest) TrackDuration(track string) clock.Time {
	var start, end clock.Time
	have := false
	for _, it := range m.Items {
		if it.Track != track {
			continue
		}
		if !have {
			start = it.PTS
			have = true
		}
		if it.PTS < start {
			start = it.PTS
		}
		if e := it.PTS + it.Duration; e > end {
			end = e
		}
	}
	if !have {
		return 0
	}
	return end - start
}

// Tracks lists the track names in first appearance order.
func (m *Manifest) Tracks() []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range m.Items {
		if !seen[it.Track] {
			seen[it.Track] = true
			out = append(out, it.Track)
		}
	}
	return out
}

// legacyDoc is the v1 on-disk shape.
type legacyDoc struct {
	Version int           `json:"version"`
	Entries []legacyEntry `json:"entries"`
}

type legacyEntry struct {
	ID         string `json:"id"`
	Codec      string `json:"codec"`
	Path       string `json:"path"`
	TimeMS     int64  `json:"time_ms"`
	DurationMS int64  `json:"duration_ms"`
}

// currentDoc is the v2 on-disk shape.
type currentDoc struct {
	Version int            `json:"version"`
	Tracks  []currentTrack `json:"tracks"`
	Entries []currentEntry `json:"entries"`
}

type currentTrack struct {
	Name      string `json:"name"`
	Timescale int64  `json:"timescale"`
}

type currentEntry struct {
	ID       string `json:"id"`
	Codec    string `json:"codec"`
	Path     string `json:"path"`
	Track    string `json:"track"`
	PTS      int64  `json:"pts"`
	Duration int64  `json:"duration"`
}

// probeHeader extracts only the version so the right decoder can be chosen.
type probeHeader struct {
	Version int `json:"version"`
}

// Decode parses either supported schema and normalises every timestamp to
// nanoseconds. It rejects unknown versions, empty manifests, duplicate IDs,
// and timestamps that cannot be represented exactly.
func Decode(data []byte) (*Manifest, error) {
	if len(data) == 0 {
		return nil, fault.Newf(fault.CodeManifestEmpty, "", "manifest is empty")
	}
	var hdr probeHeader
	if err := json.Unmarshal(data, &hdr); err != nil {
		return nil, fault.Newf(fault.CodeManifestMalformed, "", "manifest is not valid JSON: %v", err)
	}
	var m *Manifest
	var err error
	switch Version(hdr.Version) {
	case VersionLegacy:
		m, err = decodeLegacy(data)
	case VersionCurrent:
		m, err = decodeCurrent(data)
	default:
		return nil, fault.Newf(fault.CodeManifestVersion, "",
			"unsupported manifest version %d; supported versions are %v", hdr.Version, SupportedVersions())
	}
	if err != nil {
		return nil, err
	}
	if err := validate(m); err != nil {
		return nil, err
	}
	return m, nil
}

func decodeLegacy(data []byte) (*Manifest, error) {
	var doc legacyDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fault.Newf(fault.CodeManifestMalformed, "", "v1 manifest: %v", err)
	}
	// v1 times are integer milliseconds. Passing the value through a
	// timescale keeps the arithmetic identical to v2 and lets clock reject
	// overflow rather than silently wrapping.
	m := &Manifest{SourceVersion: VersionLegacy, Items: make([]Item, 0, len(doc.Entries))}
	for _, e := range doc.Entries {
		pts, err := clock.FromTimescale(e.TimeMS, 1000)
		if err != nil {
			return nil, fault.Newf(fault.CodeManifestMalformed, "", "v1 entry %q time: %v", e.ID, err)
		}
		dur, err := clock.FromTimescale(e.DurationMS, 1000)
		if err != nil {
			return nil, fault.Newf(fault.CodeManifestMalformed, "", "v1 entry %q duration: %v", e.ID, err)
		}
		m.Items = append(m.Items, Item{
			ID: e.ID, Codec: e.Codec, Path: e.Path, Track: "video",
			PTS: pts, Duration: dur,
			SourcePTS: fmt.Sprintf("%d", e.TimeMS), SourceUnit: "ms",
		})
	}
	return m, nil
}

func decodeCurrent(data []byte) (*Manifest, error) {
	var doc currentDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fault.Newf(fault.CodeManifestMalformed, "", "v2 manifest: %v", err)
	}
	tracks := map[string]int64{}
	for _, t := range doc.Tracks {
		if t.Timescale <= 0 {
			return nil, fault.Newf(fault.CodeManifestMalformed, "", "v2 track %q has timescale %d", t.Name, t.Timescale)
		}
		tracks[t.Name] = t.Timescale
	}
	m := &Manifest{SourceVersion: VersionCurrent, Items: make([]Item, 0, len(doc.Entries))}
	for _, e := range doc.Entries {
		ts, ok := tracks[e.Track]
		if !ok {
			return nil, fault.Newf(fault.CodeManifestMalformed, "", "v2 entry %q names unknown track %q", e.ID, e.Track)
		}
		pts, err := clock.FromTimescale(e.PTS, uint32(ts))
		if err != nil {
			return nil, fault.Newf(fault.CodeManifestMalformed, "", "v2 entry %q pts: %v", e.ID, err)
		}
		dur, err := clock.FromTimescale(e.Duration, uint32(ts))
		if err != nil {
			return nil, fault.Newf(fault.CodeManifestMalformed, "", "v2 entry %q duration: %v", e.ID, err)
		}
		m.Items = append(m.Items, Item{
			ID: e.ID, Codec: e.Codec, Path: e.Path, Track: e.Track,
			PTS: pts, Duration: dur,
			SourcePTS: fmt.Sprintf("%d", e.PTS), SourceUnit: "tick",
		})
	}
	return m, nil
}

func validate(m *Manifest) error {
	if len(m.Items) == 0 {
		return fault.Newf(fault.CodeManifestEmpty, "", "manifest has no entries")
	}
	seen := make(map[string]bool, len(m.Items))
	for _, it := range m.Items {
		if strings.TrimSpace(it.ID) == "" {
			return fault.Newf(fault.CodeManifestMalformed, "", "manifest entry has no id")
		}
		if seen[it.ID] {
			return fault.Newf(fault.CodeManifestDuplicateID, "", "duplicate manifest entry id %q", it.ID)
		}
		seen[it.ID] = true
		if it.PTS < 0 || it.Duration < 0 {
			return fault.Newf(fault.CodeManifestMalformed, "", "entry %q has a negative time", it.ID)
		}
	}
	return nil
}

// Sorted returns items ordered by track then presentation time. It is stable
// so equal timestamps keep their manifest order.
func (m *Manifest) Sorted() []Item {
	out := make([]Item, len(m.Items))
	copy(out, m.Items)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Track != out[j].Track {
			return out[i].Track < out[j].Track
		}
		if out[i].PTS != out[j].PTS {
			return out[i].PTS < out[j].PTS
		}
		return out[i].ID < out[j].ID
	})
	return out
}
