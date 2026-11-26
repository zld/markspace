package main

// Report is the stable terminal/JSON result for one acceptance case.
type Report struct {
	Case            string         `json:"case"`
	OK              bool           `json:"ok"`
	TargetBPS       int64          `json:"target_bps"`
	ActualBPS       float64        `json:"actual_bps"`
	DeltaPct        float64        `json:"delta_pct"`
	SNRDB           float64        `json:"snr_db"`
	AlignErrSamples int            `json:"align_err_samples"`
	BadOffset       int64          `json:"bad_offset"`
	GapCount        int            `json:"gap_count"`
	SourceSHA       string         `json:"source_sha"`
	Invalidated     int            `json:"invalidated"`
	CacheHit        int            `json:"cache_hit"`
	Fragments       int            `json:"fragments"`
	Segments        int            `json:"segments"`
	OutputDir       string         `json:"out,omitempty"`
	Manifest        string         `json:"manifest,omitempty"`
	Details         map[string]any `json:"details,omitempty"`
	Error           string         `json:"error,omitempty"`
	ErrorCode       string         `json:"error_code,omitempty"`
}

func newReport(name, out string) Report {
	return Report{
		Case: name, OK: false, BadOffset: -1, OutputDir: out,
		Details: map[string]any{},
	}
}
