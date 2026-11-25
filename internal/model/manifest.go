package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const (
	ManifestSchema = "archivecheck.io/manifest/v1"
	// StageVersion is part of the reuse key. Bump it when stage semantics or
	// canonical artifact encoding changes.
	StageVersion = "stages-v1"
)

// InputManifest is the user-supplied processing request.
type InputManifest struct {
	SchemaVersion string       `json:"schema_version"`
	Description   string       `json:"description,omitempty"`
	Assets        []InputAsset `json:"assets"`
}

// InputAsset identifies one material track and its requested edit window.
type InputAsset struct {
	ID                  string         `json:"id"`
	Path                string         `json:"path"`
	TrackID             uint32         `json:"track_id"`
	StartTimestampTicks int64          `json:"start_timestamp_ticks"`
	EditIntervals       []EditInterval `json:"edit_intervals"`
}

// EditInterval uses the shared 90 kHz clock. Index is the position in the
// MP4 edit list and is preserved in errors and provenance.
type EditInterval struct {
	Index     int   `json:"index"`
	StartTick int64 `json:"start_tick"`
	EndTick   int64 `json:"end_tick"`
	Empty     bool  `json:"empty,omitempty"`
}

// CanonicalJSON is the single normalized serialization used for hashing and
// byte-for-byte repeat detection. Struct field order is fixed and no maps or
// wall-clock timestamps are used.
func CanonicalJSON(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func SHA256Bytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// HashCanonical returns the SHA-256 of canonical JSON.
func HashCanonical(v any) (string, []byte, error) {
	b, err := CanonicalJSON(v)
	if err != nil {
		return "", nil, err
	}
	return SHA256Bytes(b), b, nil
}

// Parameters are the deterministic processing parameters.
type Parameters struct {
	Bitrates []int64 `json:"bitrates"`
}

// StageTransition records one legal FSM migration.
type StageTransition struct {
	Sequence int    `json:"sequence"`
	From     string `json:"from"`
	To       string `json:"to"`
	Reason   string `json:"reason,omitempty"`
}

// RecoveryPosition preserves both the boundary requested by the plan and the
// nearest decodable keyframe actually used.
type RecoveryPosition struct {
	Bitrate       int64 `json:"bitrate"`
	FragmentIndex int   `json:"fragment_index"`
	RequestTick   int64 `json:"request_tick"`
	ActualTick    int64 `json:"actual_tick"`
	DeltaTicks    int64 `json:"delta_ticks"`
}

// Boundary is a shared-clock fragment endpoint.
type Boundary struct {
	FragmentIndex int   `json:"fragment_index"`
	StartTick     int64 `json:"start_tick"`
	EndTick       int64 `json:"end_tick"`
}

// LayerReport is the independent cursor and verification state for one
// representation.
type LayerReport struct {
	Bitrate            int64              `json:"bitrate"`
	Cursor             int                `json:"cursor"`
	ProcessedFragments int                `json:"processed_fragments"`
	Verified           bool               `json:"verified"`
	Recovery           []RecoveryPosition `json:"recovery_positions"`
	Boundaries         []Boundary         `json:"boundaries"`
}

// ArtifactSummary identifies one independently checkable product.
type ArtifactSummary struct {
	LayerReport string `json:"layer,omitempty"`
	Path        string `json:"path"`
	Kind        string `json:"kind"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
}

// ProvenanceRecord captures enough source and processing history to audit a
// product without re-running the pipeline.
type ProvenanceRecord struct {
	SourcePath         string             `json:"source_path"`
	SourceSHA256       string             `json:"source_sha256"`
	TrackID            uint32             `json:"track_id"`
	Timescale          uint32             `json:"timescale"`
	ContainerStartTick int64              `json:"container_start_tick"`
	EditZeroPointTick  int64              `json:"edit_zero_point_tick"`
	EditIntervals      []EditInterval     `json:"edit_intervals"`
	Recovery           []RecoveryPosition `json:"recovery_positions"`
	Layers             []LayerReport      `json:"layers"`
	Transitions        []StageTransition  `json:"transitions"`
	ErrorKind          string             `json:"error_kind,omitempty"`
	ErrorStage         string             `json:"error_stage,omitempty"`
	IntervalIndex      *int               `json:"interval_index,omitempty"`
}

// AssetReport contains scan, layer, and provenance output for an asset.
type AssetReport struct {
	AssetID              string            `json:"asset_id"`
	Status               string            `json:"status"`
	Timeline             TimelineSummary   `json:"timeline"`
	Layers               []LayerReport     `json:"layers"`
	Artifacts            []ArtifactSummary `json:"artifacts"`
	MaxBoundaryDiffTicks int64             `json:"max_boundary_diff_ticks"`
	Provenance           ProvenanceRecord  `json:"provenance"`
}

// TimelineSummary is the serialized three-coordinate timeline.
type TimelineSummary struct {
	Timescale             uint32 `json:"timescale"`
	ContainerStartTick    int64  `json:"container_start_tick"`
	EditZeroPointTick     int64  `json:"edit_zero_point_tick"`
	EditedStartTick       int64  `json:"edited_start_tick"`
	EditedDurationTick    int64  `json:"edited_duration_tick"`
	NonZeroStartPreserved bool   `json:"non_zero_start_preserved"`
}

// OutputManifest is the canonical acceptance and audit result.
type OutputManifest struct {
	SchemaVersion        string            `json:"schema_version"`
	StageVersion         string            `json:"stage_version"`
	RecordID             string            `json:"record_id"`
	InputSHA256          string            `json:"input_sha256"`
	Parameters           Parameters        `json:"parameters"`
	Status               string            `json:"status"`
	TransitionCount      int               `json:"transition_count"`
	StageTransitions     []StageTransition `json:"stage_transitions"`
	Assets               []AssetReport     `json:"assets"`
	Artifacts            []ArtifactSummary `json:"artifacts"`
	ArtifactSetSHA256    string            `json:"artifact_set_sha256"`
	MaxBoundaryDiffTicks int64             `json:"max_boundary_diff_ticks"`
}
