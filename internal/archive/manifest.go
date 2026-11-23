// Package archive produces the recovered Matroska output, the deterministic
// archive manifest and the quality summary, and verifies archive-index
// structure with go-mp4.
package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// Manifest is the durable, reviewable record of one archive run. JSON field
// order is fixed via struct order, and every slice is sorted before
// serialization so repeated runs hash identically.
type Manifest struct {
	SchemaVersion int    `json:"schema_version"`
	Seed          int64  `json:"seed"`
	InputFile     string `json:"input_file"`
	OutputFile    string `json:"output_file"`
	OutputSHA256  string `json:"output_sha256"`
	ManifestSHA   string `json:"manifest_sha256,omitempty"`

	EventCount        int           `json:"event_count"`
	FirstNanos        int64         `json:"first_nanos"`
	LastNanos         int64         `json:"last_nanos"`
	Tracks            []TrackRecord `json:"tracks"`
	IsolatedTracks    []int64       `json:"isolated_tracks"`
	TruncatedOffsets  []int64       `json:"truncated_cluster_offsets"`
	OversizedElements []int64       `json:"oversized_elements"`

	RTP RTPRecord `json:"rtp"`

	Quality QualityRecord `json:"quality"`
	// Subjective measures we cannot compute without full decode are recorded
	// explicitly rather than fabricated.
	UnavailableMetrics []string `json:"unavailable_metrics"`
}

// TrackRecord is one surviving track's manifest entry.
type TrackRecord struct {
	TrackID       uint32 `json:"track_id"`
	Kind          string `json:"kind"`
	EventCount    int    `json:"event_count"`
	ChannelLayout string `json:"channel_layout,omitempty"`
	SampleRate    uint32 `json:"sample_rate,omitempty"`
	ColorFamily   string `json:"color_family,omitempty"`
}

// RTPRecord captures jitter-buffer counters.
type RTPRecord struct {
	Received           int      `json:"received"`
	Duplicates         int      `json:"duplicates"`
	Reordered          int      `json:"reordered"`
	RecoveredPackets   int      `json:"rec_recovered_packets"`
	UnrecoveredPackets int      `json:"unrecovered_packets"`
	MissingSequences   []uint64 `json:"missing_sequences"`
}

// QualityRecord holds the acceptance metrics in explicit units.
type QualityRecord struct {
	TargetBitrateBPS     int     `json:"target_bitrate_bps"`
	ActualBitrateBPS     int     `json:"actual_bitrate_bps"`
	BitrateDeviationPct  float64 `json:"bitrate_deviation_pct"`
	AlignmentErrorMillis float64 `json:"manifest_alignment_error_ms"`
}

// CanonicalJSON serializes a manifest with sorted slices and stable
// formatting suitable for hashing.
func CanonicalJSON(m *Manifest) ([]byte, error) {
	normalize(m)
	return json.MarshalIndent(m, "", "  ")
}

// HashManifest returns the SHA-256 hex digest of the canonical JSON.
func HashManifest(m *Manifest) (string, error) {
	raw, err := CanonicalJSON(m)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// HashBytes returns the SHA-256 hex digest of arbitrary output bytes.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// normalize sorts every order-dependent slice so output is deterministic.
func normalize(m *Manifest) {
	sort.Slice(m.Tracks, func(i, j int) bool {
		return m.Tracks[i].TrackID < m.Tracks[j].TrackID
	})
	sort.Slice(m.IsolatedTracks, func(i, j int) bool {
		return m.IsolatedTracks[i] < m.IsolatedTracks[j]
	})
	sort.Slice(m.TruncatedOffsets, func(i, j int) bool {
		return m.TruncatedOffsets[i] < m.TruncatedOffsets[j]
	})
	sort.Slice(m.OversizedElements, func(i, j int) bool {
		return m.OversizedElements[i] < m.OversizedElements[j]
	})
	sort.Slice(m.RTP.MissingSequences, func(i, j int) bool {
		return m.RTP.MissingSequences[i] < m.RTP.MissingSequences[j]
	})
}
