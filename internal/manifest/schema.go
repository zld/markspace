package manifest

// CanonicalVersion is the only schema version emitted by the current planner.
const CanonicalVersion = "3.0.0"

// Manifest is the canonical track, metadata, policy, and constraint input.
type Manifest struct {
	SchemaVersion     string             `json:"schema_version"`
	Seed              int64              `json:"seed,omitempty"`
	SourceKind        string             `json:"source_kind,omitempty"`
	TimecodeScaleNS   uint64             `json:"timecode_scale_ns,omitempty"`
	Tracks            []TrackMeta        `json:"tracks"`
	ColorObservations []ColorObservation `json:"color_observations,omitempty"`
	Constraints       Constraints        `json:"constraints"`
	ExplicitColor     map[string]string  `json:"explicit_color_policy,omitempty"`
	DefaultLFEMix     bool               `json:"default_lfe_mix"`
	Notes             []string           `json:"notes,omitempty"`
}

// TrackMeta contains the parameters needed for audio/color/bitrate planning.
type TrackMeta struct {
	ID               uint32 `json:"id"`
	Type             string `json:"type"`
	Codec            string `json:"codec,omitempty"`
	Timescale        uint32 `json:"timescale,omitempty"`
	InputSampleRate  int    `json:"input_sample_rate_hz,omitempty"`
	TargetSampleRate int    `json:"target_sample_rate_hz,omitempty"`
	Channels         int    `json:"channels,omitempty"`
	Layout           string `json:"layout,omitempty"`
	LayoutKnown      bool   `json:"layout_known"`
	Width            int    `json:"width,omitempty"`
	Height           int    `json:"height,omitempty"`
	SourceBitrateBPS int    `json:"source_bitrate_bps,omitempty"`
	FrameDurationNS  int64  `json:"frame_duration_ns,omitempty"`
	DurationNS       int64  `json:"duration_ns,omitempty"`
}

// Constraints are the explicit quality and processing budgets.
type Constraints struct {
	MinCoveragePPM int   `json:"min_coverage_ppm"`
	MaxAVSkewMS    int   `json:"max_av_skew_ms"`
	MaxBitrateBPS  int   `json:"max_bitrate_bps"`
	BudgetMS       int64 `json:"budget_ms"`
}

// ColorLevel identifies container, track, or frame metadata priority.
type ColorLevel string

const (
	ColorContainer ColorLevel = "container"
	ColorTrack     ColorLevel = "track"
	ColorFrame     ColorLevel = "frame"
)

// ColorObservation is one provenance-tagged color field value.
type ColorObservation struct {
	Field  string     `json:"field"`
	Value  string     `json:"value"`
	Level  ColorLevel `json:"level"`
	Source string     `json:"source"`
}

// MigrationReport records every adapter stage and rollback point.
type MigrationReport struct {
	OriginalVersion string   `json:"original_version"`
	FinalVersion    string   `json:"final_version"`
	Stages          []string `json:"stages"`
	RollbackPoints  []string `json:"rollback_points,omitempty"`
	Notes           []string `json:"notes,omitempty"`
}
