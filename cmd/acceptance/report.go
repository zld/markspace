package main

import "mediacheck/internal/timebase"

// Thresholds are the quantified acceptance limits.
type Thresholds struct {
	MaxDeltaPct        float64 `json:"max_delta_pct"`
	MinSNRDB           float64 `json:"min_snr_db"`
	MaxAlignErrSamples int     `json:"max_align_err_samples"`
}

func acceptanceThresholds() Thresholds {
	return Thresholds{MaxDeltaPct: 0.1, MinSNRDB: 90, MaxAlignErrSamples: 1}
}

// Report is the stable terminal/JSON result for one acceptance case.
type Report struct {
	Case            string             `json:"case"`
	OK              bool               `json:"ok"`
	Thresholds      Thresholds         `json:"thresholds"`
	TargetBPS       int64              `json:"target_bps"`
	ActualBPS       float64            `json:"actual_bps"`
	DeltaPct        float64            `json:"delta_pct"`
	SNRDB           float64            `json:"snr_db"`
	AlignErrSamples int                `json:"align_err_samples"`
	BadOffset       int64              `json:"bad_offset"`
	GapCount        int                `json:"gap_count"`
	SourceSHA       string             `json:"source_sha"`
	Invalidated     int                `json:"invalidated"`
	CacheHit        int                `json:"cache_hit"`
	Fragments       int                `json:"fragments"`
	Segments        int                `json:"segments"`
	OutputDir       string             `json:"out,omitempty"`
	Manifest        string             `json:"manifest,omitempty"`
	Details         map[string]any     `json:"details,omitempty"`
	Error           string             `json:"error,omitempty"`
	ErrorCode       string             `json:"error_code,omitempty"`
	ErrorRange      *timebase.Interval `json:"error_range,omitempty"`
}

func newReport(name, out string) Report {
	return Report{
		Case: name, OK: false, BadOffset: -1, OutputDir: out,
		Thresholds: acceptanceThresholds(),
		Details:    map[string]any{},
	}
}
