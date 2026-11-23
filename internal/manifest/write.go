// Package manifest writes a deterministic, byte-stable replay manifest and
// validates cross-stage invariants: non-negative durations, monotonic ranges
// and traceable track mapping.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"mediapipe/internal/fault"
	"mediapipe/internal/model"
)

// SegmentRecord is one manifest entry for a cut segment.
type SegmentRecord struct {
	Index        int    `json:"index"`
	TrackID      string `json:"track_id"`
	StartNanos   int64  `json:"start_ns"`
	EndNanos     int64  `json:"end_ns"`
	DurationNanos int64 `json:"duration_ns"`
	StartSample  int    `json:"start_sample"`
	EndSample    int    `json:"end_sample"`
	KeyframeCut  bool   `json:"keyframe_cut"`
	SilenceCut   bool   `json:"silence_cut"`
	VideoHash    string `json:"video_hash"`
	AudioHash    string `json:"audio_hash"`
}

// TrackRecord maps a normalized track back to its source.
type TrackRecord struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Container  string `json:"container"`
	SourceFile string `json:"source_file"`
	TrackOff   int64  `json:"track_offset"`
	Samples    int    `json:"samples"`
	OffsetNanos int64 `json:"offset_ns"`
	Rotation   int    `json:"rotation"`
	ColorReview bool  `json:"color_review"`
}

// Stats aggregates pipeline measurements surfaced to the terminal report.
type Stats struct {
	TargetKbps           int     `json:"target_kbps"`
	ActualKbps           float64 `json:"actual_kbps"`
	BitrateDeviationPct  float64 `json:"bitrate_deviation_pct"`
	AudioRMS             float64 `json:"audio_rms"`
	SilenceMs            int64   `json:"silence_ms"`
	ManifestAlignmentMs  float64 `json:"manifest_alignment_ms"`
	RecoveredUnits       int     `json:"recovered_units"`
	SourceErrorOffset    int64   `json:"source_error_offset"`
	QuarantinedPackets   int     `json:"quarantined_packets"`
	WrapCount            int     `json:"wrap_count"`
	DuplicateMatches     int     `json:"duplicate_matches"`
	Segments             int     `json:"segments"`
}

// Manifest is the top-level, deterministically serialized document.
type Manifest struct {
	SchemaVersion int             `json:"schema_version"`
	SessionID     string          `json:"session_id"`
	Tracks        []TrackRecord   `json:"tracks"`
	Segments      []SegmentRecord `json:"segments"`
	Stats         Stats           `json:"stats"`
	// ContentHash authenticates the manifest body for repeat-run equality.
	ContentHash string `json:"content_hash"`
}

// Build assembles a manifest from normalized tracks, segment records and
// stats, enforcing ordering before serialization.
func Build(sessionID string, tl *model.Timeline,
	segments []SegmentRecord, stats Stats) Manifest {
	m := Manifest{
		SchemaVersion: 1,
		SessionID:     sessionID,
		Segments:      append([]SegmentRecord(nil), segments...),
		Stats:         stats,
	}
	for _, tr := range tl.Tracks {
		m.Tracks = append(m.Tracks, TrackRecord{
			ID: tr.ID, Kind: string(tr.Kind),
			Container:  string(tr.Container),
			SourceFile: tr.SourceFile, TrackOff: tr.TrackOff,
			Samples: len(tr.Samples), OffsetNanos: tr.OffsetNanos,
			Rotation: tr.Rotation.Degrees, ColorReview: tr.Color.Review,
		})
	}
	sort.Slice(m.Tracks, func(i, j int) bool { return m.Tracks[i].ID < m.Tracks[j].ID })
	sort.Slice(m.Segments, func(i, j int) bool {
		if m.Segments[i].TrackID != m.Segments[j].TrackID {
			return m.Segments[i].TrackID < m.Segments[j].TrackID
		}
		return m.Segments[i].Index < m.Segments[j].Index
	})
	return m
}

// Validate enforces cross-stage invariants and returns non-recoverable faults
// for any violation. It never mutates the manifest.
func Validate(m Manifest, errs *fault.Batch) {
	for _, seg := range m.Segments {
		if seg.DurationNanos < 0 {
			errs.Add(fault.New(fault.KindInvariant,
				"negative segment duration in manifest",
				seg.TrackID, -1, false))
		}
		if seg.EndSample < seg.StartSample {
			errs.Add(fault.New(fault.KindInvariant,
				"non-monotonic sample range", seg.TrackID, -1, false))
		}
	}
	trackIDs := map[string]bool{}
	for _, tr := range m.Tracks {
		if trackIDs[tr.ID] {
			errs.Add(fault.New(fault.KindInvariant,
				"duplicate track id breaks mapping", tr.SourceFile,
				tr.TrackOff, false))
		}
		trackIDs[tr.ID] = true
	}
	for _, seg := range m.Segments {
		if !trackIDs[seg.TrackID] {
			errs.Add(fault.New(fault.KindInvariant,
				"segment references unknown track", seg.TrackID, -1, false))
		}
	}
}

// Seal computes the content hash over the manifest body excluding the hash
// field itself, then returns the canonical JSON bytes and the hash assigned
// inside them. Repeated runs over equal input produce byte-identical output.
func Seal(m Manifest) ([]byte, string, error) {
	hash, err := HashOf(m)
	if err != nil {
		return nil, "", err
	}
	m.ContentHash = hash
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, "", err
	}
	return body, hash, nil
}

// HashOf returns the content hash that Seal would assign, without serializing
// twice in callers that only need the equality token.
func HashOf(m Manifest) (string, error) {
	m.ContentHash = ""
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}
