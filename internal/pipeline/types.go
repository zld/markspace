// Package pipeline wires the reusable libraries (manifest, asset, timeline,
// evidence, journal) into one offline processing run. The cmd layer only
// builds a Config and calls Run; no media logic lives outside the libraries.
package pipeline

import (
	"encoding/json"
)

// Config is the complete parameterization of one run.
type Config struct {
	Seed          int64
	OutDir        string
	Corrupt       string // "" | "truncated"
	LateEventNS   int64  // when >0, one event is delayed past the watermark
	DriftRatePPM  float64
	DriftOffsetNS int64
}

// Stage names used in machine-readable error records.
const (
	StageGenerate = "generate"
	StageManifest = "manifest"
	StageFragment = "fragment"
	StageMerge    = "merge"
	StageRTP      = "rtp"
	StageClock    = "clock"
	StageTimeline = "timeline"
	StageEvidence = "evidence"
	StageJournal  = "journal"
)

// Failure is the stable, machine-readable error record emitted to errors.ndjson.
type Failure struct {
	Code       string `json:"code"`
	Stage      string `json:"stage"`
	Asset      string `json:"asset"`
	Track      string `json:"track,omitempty"`
	ByteOffset int64  `json:"byte_offset,omitempty"`
	Line       int    `json:"line,omitempty"`
	Message    string `json:"message"`
}

func (f Failure) jsonLine() []byte {
	b, _ := json.Marshal(f)
	return append(b, '\n')
}

// Result is the full terminal summary of a run.
type Result struct {
	Seed             int64      `json:"seed"`
	ManifestSegments int        `json:"manifest_segments"`
	MergedIntervals  int        `json:"merged_intervals"`
	Gaps             int        `json:"gap_count"`
	Conflicts        int        `json:"conflict_count"`
	FrameHashes      []string   `json:"frame_sha256"`
	PixelMeanRGB     [3]float64 `json:"pixel_mean_rgb"`
	AudioPeakDBFS    float64    `json:"audio_peak_dbfs"`
	AudioSilence     bool       `json:"audio_silence"`
	AlignmentErrorNS int64      `json:"alignment_error_ns"`
	WatermarkNS      int64      `json:"watermark_ns"`
	ReplayDigest     string     `json:"replay_digest"`
	LateReplayCount  int        `json:"late_replay_count"`
	RollbackFromNS   int64      `json:"rollback_from_ns,omitempty"`
	ValidFrames      int        `json:"valid_frames"`
	Failures         []Failure  `json:"failures"`
}
