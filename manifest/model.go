// Package manifest builds and verifies the publication manifest.
//
// The manifest deterministically describes every representation and audio
// segment in a publication: per-track start/end in the master timebase,
// per-segment continuity, target audio parameters and each segment's output
// digest. Rebuilding from identical inputs and parameters produces an
// identical manifest digest; verification rejects gaps, overlaps,
// out-of-range times and digest mismatches with a non-zero result.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// Version is the manifest schema version.
const Version = 1

// Format is the container format copied from the source asset.
type Format string

const (
	FormatMP4 Format = "mp4"
	FormatMXF Format = "mxf"
)

// Crop mirrors the media crop in pixels.
type Crop struct {
	Width       uint32 `json:"width"`
	Height      uint32 `json:"height"`
	HorizOffset int32  `json:"horiz_offset"`
	VertOffset  int32  `json:"vert_offset"`
}

// Colour mirrors the media colour description.
type Colour struct {
	Primaries uint16 `json:"primaries"`
	Transfer  uint16 `json:"transfer"`
	Matrix    uint16 `json:"matrix"`
	FullRange bool   `json:"full_range"`
}

// Render describes one representation's video presentation, after the
// documented priority rules have been applied and conflicts counted.
type Render struct {
	Width    uint32  `json:"width"`
	Height   uint32  `json:"height"`
	Rotation int     `json:"rotation"`
	Crop     *Crop   `json:"crop,omitempty"`
	Colour   *Colour `json:"colour,omitempty"`
}

// AudioTarget is the published audio parameter set.
type AudioTarget struct {
	SampleRate   uint32 `json:"sample_rate"`
	Layout       string `json:"layout"`
	Channels     uint16 `json:"channels"`
	LoudnessLUFS string `json:"loudness_lufs"`
	GainDB       string `json:"gain_db"`
}

// Representation is one video track in a publication.
type Representation struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	TrackID   uint32 `json:"track_id"`
	StartTick int64  `json:"start_tick"`
	EndTick   int64  `json:"end_tick"`
	Timescale uint32 `json:"timescale"`
	Duration  int64  `json:"duration"`
	Render    Render `json:"render"`
}

// Segment is one audio block in the published timeline.
type Segment struct {
	Index     int    `json:"index"`
	TrackID   uint32 `json:"track_id"`
	StartTick int64  `json:"start_tick"`
	EndTick   int64  `json:"end_tick"`
	Frames    int    `json:"frames"`
	Digest    string `json:"digest"`
	Bytes     int    `json:"bytes"`
}

// Track is the published audio representation.
type Track struct {
	ID        uint32      `json:"id"`
	Kind      string      `json:"kind"`
	Timescale uint32      `json:"timescale"`
	StartTick int64       `json:"start_tick"`
	EndTick   int64       `json:"end_tick"`
	Audio     AudioTarget `json:"audio"`
	Segments  []Segment   `json:"segments"`
	Conflicts int         `json:"conflicts"`
}

// Summary carries the run counters that acceptance checks inspect.
type Summary struct {
	Stages           int `json:"stages"`
	QueuePeakBytes   int `json:"queue_peak_bytes"`
	QueuePeakBlocks  int `json:"queue_peak_blocks"`
	ManifestSegments int `json:"manifest_segments"`
	ContinuityErrors int `json:"continuity_errors"`
	TimestampErrors  int `json:"timestamp_errors"`
	CacheRejected    int `json:"cache_rejected"`
	RecomputedBlocks int `json:"recomputed_blocks"`
	ResumeBlock      int `json:"resume_block"`
	Conflicts        int `json:"conflicts"`
	TruncatedBlocks  int `json:"truncated_blocks"`
}

// Manifest is the full publication record.
type Manifest struct {
	Version         int              `json:"version"`
	Source          string           `json:"source"`
	Format          Format           `json:"format"`
	InputDigest     string           `json:"input_digest"`
	MasterTimescale int64            `json:"master_timescale"`
	Representations []Representation `json:"representations"`
	Track           Track            `json:"audio_track"`
	Summary         Summary          `json:"summary"`
	// Digest is the SHA-256 of the canonical JSON with this field cleared.
	Digest string `json:"digest"`
}

// canonicalDigest computes the manifest digest over a canonical serialization
// in which Digest is cleared and segments are sorted by index. json.Marshal
// emits struct fields in declaration order, so the bytes are stable.
func (m *Manifest) canonicalDigest() (string, error) {
	clone := *m
	clone.Digest = ""
	segs := append([]Segment(nil), clone.Track.Segments...)
	sort.Slice(segs, func(i, j int) bool { return segs[i].Index < segs[j].Index })
	clone.Track.Segments = segs
	data, err := json.Marshal(clone)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Finalize computes and stores the manifest digest.
func (m *Manifest) Finalize() error {
	d, err := m.canonicalDigest()
	if err != nil {
		return err
	}
	m.Digest = d
	return nil
}

// Encode returns the canonical indented JSON bytes.
func (m *Manifest) Encode() ([]byte, error) {
	if m.Digest == "" {
		if err := m.Finalize(); err != nil {
			return nil, err
		}
	}
	return json.MarshalIndent(m, "", "  ")
}

// Blank returns an empty manifest ready for building.
func Blank(source string, format Format, inputDigest string) *Manifest {
	return &Manifest{
		Version:         Version,
		Source:          source,
		Format:          format,
		InputDigest:     inputDigest,
		MasterTimescale: 90000,
	}
}

// String summarises the manifest for terminal output.
func (m *Manifest) String() string {
	return fmt.Sprintf("%s v%d reps=%d segments=%d digest=%s",
		m.Source, m.Version, len(m.Representations), len(m.Track.Segments), m.Digest)
}
