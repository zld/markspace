// Package model holds the immutable vocabulary shared by the ingest, timeline
// and archive subsystems: assets, tracks, chunk references and stage records.
package model

import (
	"fmt"
	"sort"

	"example.com/mediavault/internal/clock"
)

// Stage is the lifecycle position of an asset. The names are the CLI contract.
type Stage string

const (
	StageReceived      Stage = "RECEIVED"
	StageScanned       Stage = "SCANNED"
	StageFingerprinted Stage = "FINGERPRINTED"
	StageNormalized    Stage = "NORMALIZED"
	StageArchived      Stage = "ARCHIVED"
)

// Order gives a stable sort position; unknown stages sort last.
func (s Stage) Order() int {
	switch s {
	case StageReceived:
		return 0
	case StageScanned:
		return 1
	case StageFingerprinted:
		return 2
	case StageNormalized:
		return 3
	case StageArchived:
		return 4
	default:
		return 5
	}
}

// Transport describes where an asset's samples came from.
type Transport string

const (
	TransportWAV Transport = "wav"
	TransportRTP Transport = "rtp"
)

// WAVFormat is the parsed fmt-chunk description of an uncompressed WAV file.
type WAVFormat struct {
	SampleRate    clock.Rate
	Channels      int
	BitsPerSample int
}

// FrameBytes returns the size of one interleaved frame in bytes.
func (f WAVFormat) FrameBytes() int { return f.Channels * ((f.BitsPerSample + 7) / 8) }

// Validate checks the supported envelope: 16/24/32-bit, 1-8 channels and 8-192 kHz.
func (f WAVFormat) Validate() error {
	if f.Channels < 1 || f.Channels > 8 {
		return fmt.Errorf("channels %d outside 1-8", f.Channels)
	}
	switch f.BitsPerSample {
	case 16, 24, 32:
	default:
		return fmt.Errorf("bits per sample %d outside 16/24/32", f.BitsPerSample)
	}
	if !f.SampleRate.Valid() {
		return fmt.Errorf("invalid sample rate")
	}
	hz := f.SampleRate.Num / f.SampleRate.Den
	if hz < 8000 || hz > 192000 {
		return fmt.Errorf("sample rate %d outside 8-192 kHz", hz)
	}
	return nil
}

// Track carries timing facts required to place an asset on the shared base.
type Track struct {
	ID             string     `json:"id"`
	Transport      Transport  `json:"transport"`
	Format         WAVFormat  `json:"-"`
	SampleRate     clock.Rate `json:"sample_rate"`
	Channels       int        `json:"channels"`
	BitsPerSample  int        `json:"bits_per_sample"`
	TotalFrames    int64      `json:"total_frames"`
	StartTimestamp uint32     `json:"start_timestamp,omitempty"`
	ClockRate      uint32     `json:"clock_rate,omitempty"`
}

// ChunkRef records one content-addressed block of derived PCM.
type ChunkRef struct {
	Index  int    `json:"index"`
	Offset int64  `json:"offset"`
	Frames int64  `json:"frames"`
	Length int64  `json:"length"`
	Digest string `json:"digest"`
}

// Asset is the unit indexed by mediavault. Paths are observations only; the
// ContentDigest is what decides identity.
type Asset struct {
	Key              string     `json:"key"`
	Name             string     `json:"name"`
	Path             string     `json:"path"`
	Transport        Transport  `json:"transport"`
	SourceDigest     string     `json:"source_digest"`
	ContentDigest    string     `json:"content_digest"`
	ContainerDigest  string     `json:"container_digest"`
	PolicyVersion    string     `json:"policy_version"`
	ChunkFrames      int64      `json:"chunk_frames"`
	Chunks           []ChunkRef `json:"chunks"`
	Track            Track      `json:"track"`
	Stage            Stage      `json:"stage"`
	Rejected         bool       `json:"rejected"`
	RejectionCode    string     `json:"rejection_code,omitempty"`
	ArchiveDigest    string     `json:"archive_digest,omitempty"`
	CommittedBlocks  int        `json:"committed_blocks"`
	RecomputedBlocks int        `json:"recomputed_blocks"`
	ReusedBlocks     int        `json:"reused_blocks"`
}

// SortedChunks returns the chunks ordered by index without mutating the asset.
func (a Asset) SortedChunks() []ChunkRef {
	out := append([]ChunkRef(nil), a.Chunks...)
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

// TotalLength sums the stored chunk lengths.
func (a Asset) TotalLength() int64 {
	var n int64
	for _, c := range a.Chunks {
		n += c.Length
	}
	return n
}

// AssetKey is the stable manifest sort key and the deduplication identity. It
// is built from the transport and the content digest only: neither the path, the
// file name nor the API/container framing may promote a re-import to a new
// asset. Identical PCM re-wrapped in another RIFF layout collapses onto the
// same key.
func AssetKey(transport Transport, contentDigest string) string {
	return fmt.Sprintf("%s:%s", transport, contentDigest)
}
