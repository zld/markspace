package verify

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"mediacore/internal/timeline"
	"mediacore/internal/transform"
)

// SourceEntry records one input file and its observed digest.
type SourceEntry struct {
	Name         string  `json:"name"`
	Size         int64   `json:"size"`
	SHA256       string  `json:"sha256"`
	Fragments    int     `json:"fragments"`
	ErrorOffsets []int64 `json:"error_offsets,omitempty"`
}

// TrackEntry records one track's mapped timebase and sample span.
type TrackEntry struct {
	TrackID           uint32 `json:"track_id"`
	Kind              string `json:"kind"`
	SourceTimescale   uint32 `json:"source_timescale"`
	TimelineTimescale uint32 `json:"timeline_timescale"`
	SourceStart       int64  `json:"source_start"`
	TimelineStart     int64  `json:"timeline_start"`
	SampleCount       int    `json:"sample_count"`
	Mergeable         bool   `json:"mergeable"`
}

// FrameEntry is the per-frame provenance recorded in the manifest.
type FrameEntry struct {
	Number       int    `json:"number"`
	TrackID      uint32 `json:"track_id"`
	TimelineTime int64  `json:"timeline_time"`
	PixelSHA     string `json:"pixel_sha256"`
	SourceFile   string `json:"source_file"`
	SourceOffset int64  `json:"source_offset"`
	SourceSize   int64  `json:"source_size"`
	SourceSHA    string `json:"source_sha256"`
}

// Manifest records where every output came from.
type Manifest struct {
	Version        int           `json:"version"`
	Seed           int64         `json:"seed"`
	ConfigDigest   string        `json:"config_digest"`
	InputDigest    string        `json:"input_digest"`
	Sources        []SourceEntry `json:"sources"`
	Tracks         []TrackEntry  `json:"tracks"`
	Frames         []FrameEntry  `json:"frames"`
	FrameDigest    string        `json:"frame_digest"`
	TimelineDigest string        `json:"timeline_digest"`
	FaultCodes     []string      `json:"fault_codes,omitempty"`
	// SHA256 is the digest of the manifest body, filled by Write.
	SHA256 string `json:"sha256,omitempty"`
}

// NewManifest builds the source list from observed inputs.
func NewManifest(seed int64, configDigest, inputDigest string, sources []SourceEntry) *Manifest {
	return &Manifest{
		Version:      1,
		Seed:         seed,
		ConfigDigest: configDigest,
		InputDigest:  inputDigest,
		Sources:      sources,
	}
}

// AddTracks records every track's timebase mapping.
func (m *Manifest) AddTracks(tracks []*timeline.Track) {
	for _, tr := range tracks {
		m.Tracks = append(m.Tracks, TrackEntry{
			TrackID:           tr.ID,
			Kind:              tr.Kind,
			SourceTimescale:   tr.Mapping.SourceTimescale,
			TimelineTimescale: tr.Mapping.TimelineTimescale,
			SourceStart:       tr.Mapping.SourceStart,
			TimelineStart:     tr.Mapping.TimelineStart,
			SampleCount:       len(tr.Samples),
			Mergeable:         tr.Mergeable,
		})
	}
}

// AddFrames records the exported frame sequence.
func (m *Manifest) AddFrames(set *transform.FrameSet) {
	m.FrameDigest = set.Digest
	for _, f := range set.Frames {
		m.Frames = append(m.Frames, FrameEntry{
			Number:       f.Number,
			TrackID:      f.TrackID,
			TimelineTime: f.TimelineTime,
			PixelSHA:     f.PixelSHA,
			SourceFile:   f.SourceFile,
			SourceOffset: f.SourceOffset,
			SourceSize:   f.SourceSize,
			SourceSHA:    f.SourceSHA,
		})
	}
}

// SetTimelineDigest records a digest over the merged timeline facts.
func (m *Manifest) SetTimelineDigest(samples []timeline.Sample) {
	h := sha256.New()
	for _, s := range samples {
		fmt.Fprintf(h, "%d|%d|%d|%d|%s|%d|%d\n", s.TrackID, s.TimelineTime, s.TimelineDuration, s.SourceTime, s.File, s.FileOffset, s.ByteLength)
	}
	m.TimelineDigest = fmt.Sprintf("%x", h.Sum(nil))
}

// AddFaults records the distinct fault codes seen.
func (m *Manifest) AddFaults(errs interface{ Codes() []string }) {
	m.FaultCodes = append(m.FaultCodes, errs.Codes()...)
	sort.Strings(m.FaultCodes)
}

// SHA256Digest returns the manifest self digest, computing it if the manifest
// has not been written yet.
func (m *Manifest) SHA256Digest() string {
	if m.SHA256 != "" {
		return m.SHA256
	}
	return m.Digest()
}

// Digest hashes the manifest body excluding the SHA256 field.
func (m *Manifest) Digest() string {
	clone := *m
	clone.SHA256 = ""
	data, err := json.Marshal(clone)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}

// Write saves the manifest with its self digest.
func (m *Manifest) Write(path string) error {
	m.SHA256 = m.Digest()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// ReadManifest loads a manifest and verifies its self digest.
func ReadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m.SHA256 != "" && m.SHA256 != m.Digest() {
		return &m, fmt.Errorf("manifest: self digest mismatch")
	}
	return &m, nil
}
