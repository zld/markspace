package pipeline

import (
	"mediacore/internal/index"
	"mediacore/internal/mp4"
)

// Segment is a deterministic query-aligned fragment.
type Segment struct {
	Index          int     `json:"index"`
	StartNS        int64   `json:"start_ns"`
	EndNS          int64   `json:"end_ns"`
	KeyframeNumber int     `json:"keyframe_number"`
	KeyframeOffset int64   `json:"keyframe_offset"`
	SampleNumbers  []int   `json:"sample_numbers"`
	SampleOffsets  []int64 `json:"sample_offsets"`
	ContentSHA256  string  `json:"content_sha256"`
}

// Report contains all acceptance metrics and recovery counters.
type Report struct {
	BatchID               string        `json:"batch_id"`
	ManifestPath          string        `json:"manifest_path"`
	Range                 index.Range   `json:"range"`
	QuerySampleCount      int           `json:"query_sample_count"`
	KeyframeNumber        int           `json:"keyframe_number"`
	ParseRoundtripRate    float64       `json:"parse_roundtrip_rate"`
	KeyframeHitRate       float64       `json:"keyframe_hit_rate"`
	SegmentHashes         []string      `json:"segment_hashes"`
	RecoveredArtifacts    int           `json:"recovered_artifacts"`
	DuplicatePublications int           `json:"duplicate_publications"`
	RegeneratedArtifacts  int           `json:"regenerated_artifacts"`
	PublishWrites         int           `json:"publish_writes"`
	ErrorOffsets          []ErrorOffset `json:"error_offsets"`
}

// ErrorOffset is the stable CLI error diagnostic row.
type ErrorOffset struct {
	Category string `json:"category"`
	Path     string `json:"path"`
	Track    int    `json:"track_number"`
	Offset   int64  `json:"raw_offset"`
}

type analysis struct {
	manifest *mp4.Manifest
	parsed   []mp4.ParsedSource
	indexes  map[string]*index.Index
	primary  *index.Index
	result   *index.QueryResult
	segments []Segment
}
