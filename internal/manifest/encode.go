package manifest

import (
	"encoding/json"
	"sort"

	"archivecheck/internal/fault"
)

// normalise replaces nil collections with empty ones so a manifest for a
// source with no damage or no tracks encodes identically every time.
func normalise(m *Manifest) {
	if m.Sources == nil {
		m.Sources = []SourceRef{}
	}
	if m.Tracks == nil {
		m.Tracks = []TrackRef{}
	}
	if m.Damage == nil {
		m.Damage = []Damage{}
	}
	if m.TimeRanges == nil {
		m.TimeRanges = []TimeRange{}
	}
	if m.Derivatives == nil {
		m.Derivatives = []Derivative{}
	}
}

// sortManifest puts every collection in the stable order used for checksums.
func sortManifest(m *Manifest) {
	sort.SliceStable(m.Sources, func(i, j int) bool {
		if m.Sources[i].Digest != m.Sources[j].Digest {
			return m.Sources[i].Digest < m.Sources[j].Digest
		}
		return m.Sources[i].Name < m.Sources[j].Name
	})
	sort.SliceStable(m.Tracks, func(i, j int) bool {
		if m.Tracks[i].Source != m.Tracks[j].Source {
			return m.Tracks[i].Source < m.Tracks[j].Source
		}
		if m.Tracks[i].ID != m.Tracks[j].ID {
			return m.Tracks[i].ID < m.Tracks[j].ID
		}
		return m.Tracks[i].Index < m.Tracks[j].Index
	})
	sort.SliceStable(m.Damage, func(i, j int) bool {
		a, c := m.Damage[i], m.Damage[j]
		if a.Source != c.Source {
			return a.Source < c.Source
		}
		if a.Start != c.Start {
			return a.Start < c.Start
		}
		if a.End != c.End {
			return a.End < c.End
		}
		return a.Kind < c.Kind
	})
	sort.SliceStable(m.TimeRanges, func(i, j int) bool {
		a, c := m.TimeRanges[i], m.TimeRanges[j]
		if a.Source != c.Source {
			return a.Source < c.Source
		}
		if a.Track != c.Track {
			return a.Track < c.Track
		}
		return a.Start < c.Start
	})
	sort.SliceStable(m.Derivatives, func(i, j int) bool {
		a, c := m.Derivatives[i], m.Derivatives[j]
		if a.Kind != c.Kind {
			return a.Kind < c.Kind
		}
		if a.Key != c.Key {
			return a.Key < c.Key
		}
		return a.Digest < c.Digest
	})
}

func (m *Manifest) encode() ([]byte, error) {
	sum, err := m.Checksum()
	if err != nil {
		return nil, err
	}
	doc := document{
		body: body{
			Version: m.Version, Sources: m.Sources, Tracks: m.Tracks,
			Damage: m.Damage, TimeRanges: m.TimeRanges, Derivatives: m.Derivatives,
		},
		Counts: m.Counts, Checksum: sum,
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fault.Wrap(err, fault.StageManifest, fault.KindCorrupt, "manifest.json", 0, "encode manifest")
	}
	return append(out, '\n'), nil
}

// Bytes renders the manifest exactly as it is written to disk.
func (m *Manifest) Bytes() ([]byte, error) { return m.encode() }
