// Package quality aggregates the statistics reported by mediacheck.
package quality

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"

	"mediaforge/internal/mediaerr"
)

// Range is a half-open sample interval.
type Range struct {
	Start int64 `json:"start_sample"`
	End   int64 `json:"end_sample"`
}

// BadRecord documents one rejected record so an operator can locate it.
type BadRecord struct {
	Offset  int64  `json:"byte_offset"`
	Seq     uint16 `json:"seq"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Stats is the result document emitted by `mediacheck run`.
type Stats struct {
	Packets                 int64   `json:"packets"`
	Loss                    int64   `json:"loss"`
	Duplicates              int64   `json:"duplicates"`
	LateReplays             int64   `json:"late_replays"`
	BadPackets              int64   `json:"bad_packets"`
	InputBitrateBps         float64 `json:"input_bitrate_bps"`
	TargetBitrateBps        float64 `json:"target_bitrate_bps"`
	BitrateErrorPct         float64 `json:"bitrate_error_pct"`
	InputLUFS               float64 `json:"input_lufs"`
	OutputLUFS              float64 `json:"output_lufs"`
	PeakDBFS                float64 `json:"peak_dbfs"`
	ClippedSamples          int64   `json:"clipped_samples"`
	InputPeakDBFS           float64 `json:"input_peak_dbfs"`
	InputClippedSamples     int64   `json:"input_clipped_samples"`
	ManifestAlignmentSample int64   `json:"manifest_alignment_samples"`
	OutputSHA256            string  `json:"output_sha256"`
	// Extra diagnostics that are useful to verify but not part of the
	// required output contract.
	UniquePackets      int64       `json:"unique_packets"`
	TruncatedPackets   int64       `json:"truncated_packets"`
	CorruptPayloads    int64       `json:"corrupt_payloads"`
	InputBytes         int64       `json:"input_bytes"`
	OutputBytes        int64       `json:"output_bytes"`
	OutputSamples      int64       `json:"output_samples"`
	ManifestSamples    int64       `json:"manifest_samples"`
	OutputDurationSecs float64     `json:"output_duration_seconds"`
	IndexComplete      bool        `json:"index_complete"`
	BadDetails         []BadRecord `json:"bad_details,omitempty"`
}

// BitrateError computes the signed percentage difference from target.
func BitrateError(input, target float64) float64 {
	if target == 0 {
		return 0
	}
	return (input - target) / target * 100
}

// AlignmentSamples returns the absolute difference between two sample counts.
func AlignmentSamples(a, b int64) int64 {
	d := a - b
	if d < 0 {
		return -d
	}
	return d
}

// HashFile returns the lowercase hex SHA-256 of a local file.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", mediaerr.New(mediaerr.CodeIO, "open %s: %v", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", mediaerr.New(mediaerr.CodeIO, "hash %s: %v", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Finite makes non-finite loudness values serializable as null in JSON.
func Finite(v float64) *float64 {
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return nil
	}
	return &v
}

// Validate checks the contractual tolerances. Bitrate tolerance is 5 % and
// manifest alignment must not exceed one sample. Bitrate is only checked when
// a target is supplied.
func (s Stats) Validate(targetBitrate float64, checkBitrate bool) error {
	if checkBitrate && math.Abs(s.BitrateErrorPct) > 5 {
		return mediaerr.New(mediaerr.CodeVerify,
			"bitrate deviation %.2f%% exceeds 5%%", s.BitrateErrorPct)
	}
	if s.ManifestAlignmentSample > 1 {
		return mediaerr.New(mediaerr.CodeVerify,
			"manifest alignment %d samples exceeds 1", s.ManifestAlignmentSample)
	}
	return nil
}

// Summary renders the stats for the terminal.
func (s Stats) Summary() string {
	lufs := func(v float64) string {
		if math.IsInf(v, 0) {
			return "-inf"
		}
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf(
		"packets=%d unique=%d loss=%d duplicates=%d late_replays=%d bad_packets=%d\n"+
			"input_bitrate_bps=%.0f target_bitrate_bps=%.0f bitrate_error_pct=%.2f\n"+
			"input_lufs=%s output_lufs=%s peak_dbfs=%.2f clipped_samples=%d\n"+
			"input_peak_dbfs=%.2f input_clipped_samples=%d\n"+
			"manifest_alignment_samples=%d output_sha256=%s",
		s.Packets, s.UniquePackets, s.Loss, s.Duplicates, s.LateReplays, s.BadPackets,
		s.InputBitrateBps, s.TargetBitrateBps, s.BitrateErrorPct,
		lufs(s.InputLUFS), lufs(s.OutputLUFS), s.PeakDBFS, s.ClippedSamples,
		s.InputPeakDBFS, s.InputClippedSamples,
		s.ManifestAlignmentSample, s.OutputSHA256)
}
