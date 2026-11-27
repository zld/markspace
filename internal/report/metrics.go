// Package report turns pipeline, manifest and quality measurements into one
// stable, machine-readable summary. The JSON keys are part of the acceptance
// contract and are kept in the snake_case form the demo command prints.
package report

import (
	"math"

	"github.com/zld/continuity/internal/index"
	"github.com/zld/continuity/internal/manifest"
)

// Summary is the single record printed by the command entry point.
type Summary struct {
	TargetBitrate       int64   `json:"target_bitrate"`
	ActualBitrate       int64   `json:"actual_bitrate"`
	BitrateDeviationPct float64 `json:"bitrate_deviation_pct"`
	PSNRDB              float64 `json:"psnr_db"`
	ManifestMaxErrorMS  float64 `json:"manifest_max_error_ms"`
	RepairedTimestamps  int     `json:"repaired_timestamps"`
	IndexEntries        int     `json:"index_entries"`
	SyncEntries         int     `json:"sync_entries"`
	CheckpointResumed   bool    `json:"checkpoint_resumed"`
	ResumedBlocks       int     `json:"resumed_blocks"`
	PeakQueueChunks     int     `json:"peak_queue_chunks"`
	ManifestBackward    int     `json:"manifest_backward"`
	ManifestMissing     int     `json:"manifest_missing"`
	ManifestCoverage    int     `json:"manifest_coverage"`
}

// Inputs carries the measurements the summary is assembled from.
type Inputs struct {
	TargetBitrate      int64
	ActualBitrate      int64
	PSNRDB             float64
	RepairedTimestamps int
	PeakQueueChunks    int
	CheckpointResumed  bool
	ResumedBlocks      int
	Index              *index.Index
	Manifest           manifest.Result
}

// Build assembles the summary, deriving bitrate deviation and index counts.
func Build(in Inputs) Summary {
	s := Summary{
		TargetBitrate:      in.TargetBitrate,
		ActualBitrate:      in.ActualBitrate,
		PSNRDB:             in.PSNRDB,
		RepairedTimestamps: in.RepairedTimestamps,
		PeakQueueChunks:    in.PeakQueueChunks,
		CheckpointResumed:  in.CheckpointResumed,
		ResumedBlocks:      in.ResumedBlocks,
		ManifestMaxErrorMS: in.Manifest.MaxErrorMS,
		ManifestBackward:   in.Manifest.Counts.Backward,
		ManifestMissing:    in.Manifest.Counts.Missing,
		ManifestCoverage:   in.Manifest.Counts.Coverage,
	}
	if in.Index != nil {
		s.IndexEntries = in.Index.EntryCount()
		s.SyncEntries = in.Index.SyncCount()
	}
	s.BitrateDeviationPct = bitrateDeviation(in.TargetBitrate, in.ActualBitrate)
	return s
}

// bitrateDeviation returns the signed-free percentage difference of actual
// against target. A zero target yields zero so a misconfigured run does not
// produce NaN/Inf in the JSON output.
func bitrateDeviation(target, actual int64) float64 {
	if target <= 0 {
		return 0
	}
	return math.Abs(float64(actual-target)) / float64(target) * 100
}

// Bitrate computes bits-per-second from total sample bytes and an exact
// duration in seconds supplied as a rational.
func Bitrate(totalBytes int64, durationSeconds float64) int64 {
	if durationSeconds <= 0 {
		return 0
	}
	return int64(math.Round(float64(totalBytes*8) / durationSeconds))
}

// PSNR computes peak signal-to-noise ratio in dB for two equal-length byte
// planes. The synthetic fixtures keep every retained sample byte-identical, so
// an undamaged run reports +Inf dB; any rewritten byte lowers it measurably.
func PSNR(reference, candidate []byte, peak float64) (float64, error) {
	if len(reference) != len(candidate) || len(reference) == 0 {
		return 0, errPlaneMismatch
	}
	var se float64
	for i := range reference {
		d := float64(reference[i]) - float64(candidate[i])
		se += d * d
	}
	mse := se / float64(len(reference))
	if mse == 0 {
		return math.Inf(1), nil
	}
	if peak <= 0 {
		peak = 255
	}
	return 10 * math.Log10((peak*peak)/mse), nil
}
