package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"archive-lab/internal/quality"
)

// Report is the stable JSON contract printed by the CLI. Field order is fixed
// by the struct; missing measurements are represented by null, never by a
// fabricated number.
type Report struct {
	Status           string   `json:"status"`
	Input            string   `json:"input"`
	InputSHA256      string   `json:"input_sha256"`
	ConfigSHA256     string   `json:"config_sha256"`
	TargetBps        int64    `json:"target_bps"`
	MaxBps           int64    `json:"max_bps"`
	ActualBps        int64    `json:"actual_bps"`
	BitrateErrorPct  float64  `json:"bitrate_error_pct"`
	PSNRDB           *float64 `json:"psnr_db"`
	PSNRUnavailable  string   `json:"psnr_unavailable,omitempty"`
	PlaylistDriftMS  float64  `json:"playlist_drift_ms"`
	MinPSNR          float64  `json:"min_psnr"`
	BudgetMS         int64    `json:"budget_ms"`
	SegmentsTotal    int      `json:"segments_total"`
	SegmentsDone     int      `json:"segments_done"`
	SegmentsAdopted  []int    `json:"segments_adopted"`
	SegmentsDeferred []int    `json:"segments_deferred"`
	BytesIn          int64    `json:"bytes_in"`
	BytesOut         int64    `json:"bytes_out"`
	Codec            string   `json:"codec"`
	QuantizerMilli   int      `json:"quantizer_milli"`
	Cache            string   `json:"cache"`
	IndexRebuilt     bool     `json:"index_rebuilt"`
	IndexSHA256      string   `json:"index_sha256"`
	InputDigest      string   `json:"input_digest"`
	Unmet            []string `json:"unmet_constraints"`
	ConstraintsOK    bool     `json:"constraints_ok"`
	Notes            []string `json:"notes,omitempty"`
	ResumedFrom      int      `json:"resumed_from"`
	ReportSHA256     string   `json:"report_sha256"`
	RTPPackets       int      `json:"rtp_packets"`
	RTPDisorder      int      `json:"rtp_disorder"`
	RTPLost          int      `json:"rtp_lost"`
	FailedStage      string   `json:"failed_stage,omitempty"`
	Error            string   `json:"error,omitempty"`
}

// Digest is a stable digest of the report's measured fields, excluding the
// cache status so a recomputation from cache proves determinism.
func (r *Report) Digest() string {
	h := sha256.New()
	fmt.Fprintf(h, "input=%s\ninput_digest=%s\nconfig=%s\nstatus=%s\n",
		r.InputSHA256, r.InputDigest, r.ConfigSHA256, r.Status)
	fmt.Fprintf(h, "segments=%d/%d\nbytes=%d/%d\n", r.SegmentsDone, r.SegmentsTotal, r.BytesIn, r.BytesOut)
	fmt.Fprintf(h, "bps=%d/%d err=%.6f\n", r.ActualBps, r.TargetBps, r.BitrateErrorPct)
	if r.PSNRDB != nil {
		fmt.Fprintf(h, "psnr=%.6f\n", *r.PSNRDB)
	} else {
		fmt.Fprintf(h, "psnr=unavailable\n")
	}
	fmt.Fprintf(h, "drift=%.6f\nq=%d\nrtp=%d/%d\n", r.PlaylistDriftMS, r.QuantizerMilli, r.RTPPackets, r.RTPDisorder)
	for _, u := range r.Unmet {
		fmt.Fprintf(h, "unmet=%s\n", u)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Seal fills the self digest so two runs over identical inputs and
// configuration can be compared directly from their JSON output.
func (r *Report) Seal() { r.ReportSHA256 = r.Digest() }

// WriteJSON emits the report followed by a newline.
func (r *Report) WriteJSON(w io.Writer) error {
	r.Seal()
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		return err
	}
	return nil
}

// PSNRValue returns the measured PSNR, if one exists.
func (r *Report) PSNRValue() (float64, bool) {
	if r.PSNRDB == nil {
		return 0, false
	}
	return *r.PSNRDB, true
}

// setPSNR stores a measurement only when it is real.
func setPSNR(r *Report, v float64, known bool) {
	if !known {
		r.PSNRUnavailable = "PSNR is not computable for this source (no uncompressed luma samples)"
		return
	}
	r.PSNRDB = &v
}

var _ = quality.CodecVersion
