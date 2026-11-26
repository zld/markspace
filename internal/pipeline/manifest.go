package pipeline

import (
	"pwflow/internal/ebml"
	"pwflow/internal/fault"
	"pwflow/internal/fingerprint"
	"pwflow/internal/planner"
	pwrtp "pwflow/internal/rtp"
	"pwflow/internal/segment"
	"pwflow/internal/timebase"
)

// ManifestVersion is the stable schema written to manifest.json.
const ManifestVersion = "pwflow-v1"

// Status values returned by run and verify.
const (
	StatusPass   = "PASS"
	StatusGaps   = "PASS_WITH_GAPS"
	StatusFailed = "FAIL"
)

// Inputs records the local input paths and digests.
type Inputs struct {
	MediaPath  string `json:"media_path"`
	EventsPath string `json:"events_path"`
	RTPPath    string `json:"rtp_path"`
	MediaSHA   string `json:"media_sha256"`
	EventsSHA  string `json:"events_sha256"`
	RTPSHA     string `json:"rtp_sha256"`
	Combined   string `json:"combined_digest"`
}

// FrameInfo is one decoded video frame fingerprint and pixel statistic.
type FrameInfo struct {
	Index       int     `json:"index"`
	GlobalIndex int     `json:"global_index"`
	PTSNS       int64   `json:"pts_ns"`
	SHA256      string  `json:"sha256"`
	PixelSHA256 string  `json:"pixel_sha256"`
	LumaMean    float64 `json:"luma_mean"`
}

// AudioWindowInfo fingerprints a canonical PCM window.
type AudioWindowInfo struct {
	timebase.Interval
	SHA256  string  `json:"sha256"`
	RMSDBFS float64 `json:"rms_dbfs,omitempty"`
}

// SegmentResult contains one output slice and all per-segment observations.
type SegmentResult struct {
	Index          int                    `json:"index"`
	File           string                 `json:"file"`
	FileSHA256     string                 `json:"file_sha256"`
	FileSize       int64                  `json:"file_size"`
	Cut            segment.Cut            `json:"cut"`
	Frames         []FrameInfo            `json:"frame_hashes"`
	FrameCount     int                    `json:"frame_count"`
	FramesSHA256   string                 `json:"frames_sha256"`
	LumaMean       float64                `json:"luma_mean"`
	LoudnessPeakDB *float64               `json:"loudness_peak_dbfs,omitempty"`
	Silences       []timebase.Interval    `json:"silence_intervals"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
}

// Counts is the compact terminal/manifest count block.
type Counts struct {
	Segments         int `json:"segments"`
	SilenceIntervals int `json:"silence_intervals"`
	DuplicatePackets int `json:"duplicate_packets"`
	MissingPackets   int `json:"missing_packets"`
	EBMLDamages      int `json:"ebml_damaged_blocks"`
	DuplicateContent int `json:"duplicate_content_groups"`
	Frames           int `json:"frames"`
}

// Alignment summarizes timeline and cut alignment errors.
type Alignment struct {
	MaxErrorNS     int64   `json:"max_alignment_error_ns"`
	MaxErrorMS     float64 `json:"max_alignment_error_ms"`
	CutMaxErrorNS  int64   `json:"cut_max_error_ns"`
	MappingErrorNS int64   `json:"mapping_error_ns"`
	ThresholdMS    float64 `json:"threshold_ms"`
}

// Manifest is the deterministic product contract.
type Manifest struct {
	SchemaVersion    string              `json:"schema_version"`
	Status           string              `json:"status"`
	Seed             int64               `json:"seed,omitempty"`
	Inputs           Inputs              `json:"inputs"`
	Plan             *planner.Plan       `json:"plan,omitempty"`
	Counts           Counts              `json:"counts"`
	Segments         []SegmentResult     `json:"segments"`
	Silences         []timebase.Interval `json:"silence_intervals"`
	AudioWindows     []AudioWindowInfo   `json:"audio_windows,omitempty"`
	RTPGaps          []pwrtp.Gap         `json:"rtp_gaps"`
	EBMLDamages      []ebml.Damage       `json:"ebml_damages"`
	DuplicateGroups  []fingerprint.Group `json:"duplicate_content_groups,omitempty"`
	Alignment        Alignment           `json:"alignment"`
	LumaMean         float64             `json:"luma_mean"`
	LoudnessPeakDB   *float64            `json:"loudness_peak_dbfs"`
	BandwidthBpsEWMA float64             `json:"bandwidth_bps_ewma,omitempty"`
	Errors           []fault.Record      `json:"recoverable_errors,omitempty"`
}
