// Package manifest builds the final archive manifest and the content-addressed
// derivative store that sits under it.
//
// Derivatives are layered: layer 1 items normalise one source (a subtitle
// track, a stream analysis report), and layer 2 items combine layer 1 output.
// Every derivative is keyed by its kind, its layer and the digests of its
// dependencies, and it is stored under the digest of its own bytes. A source
// digest change therefore changes exactly the keys that depend on that source;
// nothing else is invalidated.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"archivecheck/internal/timebase"
)

// Version is the manifest schema version.
const Version = 1

// SourceRef identifies a source by digest.
type SourceRef struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// TrackRef is a manifest entry for one analysed track.
type TrackRef struct {
	Source        string         `json:"source"`
	Index         int            `json:"index"`
	ID            uint32         `json:"id"`
	Handler       string         `json:"handler"`
	Timescale     uint32         `json:"timescale"`
	DurationNanos timebase.Nanos `json:"durationNanos"`
	SampleCount   int            `json:"sampleCount"`
	SyncSamples   int            `json:"syncSamples"`
	IntervalNanos timebase.Nanos `json:"intervalNanos"`
}

// Damage records one damaged byte interval of a source.
type Damage struct {
	Source string `json:"source"`
	Start  int64  `json:"start"`
	End    int64  `json:"end"`
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
}

// TimeRange is the covered time range of a track or subtitle source.
type TimeRange struct {
	Source string         `json:"source"`
	Track  string         `json:"track,omitempty"`
	Start  timebase.Nanos `json:"start"`
	End    timebase.Nanos `json:"end"`
}

// Derivative is one stored, content-addressed artefact.
type Derivative struct {
	Kind   string   `json:"kind"`
	Layer  int      `json:"layer"`
	Key    string   `json:"key"`
	Digest string   `json:"digest"`
	Size   int64    `json:"size"`
	Path   string   `json:"path"`
	Deps   []string `json:"deps"`
}

// Counts summarises a build against the previous run.
type Counts struct {
	Added       int `json:"added"`
	Reused      int `json:"reused"`
	Changed     int `json:"changed"`
	Invalidated int `json:"invalidated"`
}

// Manifest is the stable, self-verifying archive description.
type Manifest struct {
	Version     int          `json:"version"`
	Sources     []SourceRef  `json:"sources"`
	Tracks      []TrackRef   `json:"tracks"`
	Damage      []Damage     `json:"damage"`
	TimeRanges  []TimeRange  `json:"timeRanges"`
	Derivatives []Derivative `json:"derivatives"`
	Counts      Counts       `json:"counts"`
}

// body is the checksum-covered portion of a manifest.
type body struct {
	Version     int          `json:"version"`
	Sources     []SourceRef  `json:"sources"`
	Tracks      []TrackRef   `json:"tracks"`
	Damage      []Damage     `json:"damage"`
	TimeRanges  []TimeRange  `json:"timeRanges"`
	Derivatives []Derivative `json:"derivatives"`
}

// document is what is written to disk: the body plus its checksum and counts.
type document struct {
	body
	Counts   Counts `json:"counts"`
	Checksum string `json:"checksum"`
}

// Checksum returns the SHA-256 of the canonical body encoding. The counts are
// deliberately outside the checksum: they describe the build that produced the
// manifest, not its content, so a first run and a repeat run over unchanged
// sources yield the same checksum.
func (m *Manifest) Checksum() (string, error) {
	data, err := canonical(body{
		Version: m.Version, Sources: m.Sources, Tracks: m.Tracks,
		Damage: m.Damage, TimeRanges: m.TimeRanges, Derivatives: m.Derivatives,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func canonical(v any) ([]byte, error) {
	return json.Marshal(v)
}

// Summary renders one human-readable manifest line.
func (m *Manifest) Summary() string {
	sum, err := m.Checksum()
	if err != nil {
		sum = "unavailable"
	}
	return fmt.Sprintf("manifest sources=%d tracks=%d damage=%d ranges=%d derivatives=%d checksum=%s",
		len(m.Sources), len(m.Tracks), len(m.Damage), len(m.TimeRanges), len(m.Derivatives), sum)
}
