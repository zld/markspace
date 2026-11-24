package medpipe

import (
	"encoding/json"
	"math"
)

// ManifestSchemaVersion marks the manifest JSON format.
const ManifestSchemaVersion = 1

// TrackStatus values.
const (
	StatusOK       = "ok"
	StatusDegraded = "degraded" // completed with recoverable errors
	StatusFailed   = "failed"   // stopped on a fatal error
)

// TrackResult is the per-track section of a manifest.
type TrackResult struct {
	Track         string           `json:"track"`
	Codec         Codec            `json:"codec"`
	Kind          Kind             `json:"kind"`
	Status        string           `json:"status"`
	IndexRebuilt  bool             `json:"index_rebuilt"`
	InputDigest   Digest           `json:"input_digest"`
	FrameCount    int              `json:"frame_count"`
	KeyframeCount int              `json:"keyframe_count"`
	DurationUS    int64            `json:"duration_us"`
	ClockStats    ClockStats       `json:"clock_stats"`
	Color         *ColorReport     `json:"color,omitempty"`
	Exec          *ExecStats       `json:"exec,omitempty"`
	Errors        []*PipelineError `json:"errors"`
}

// RunMetrics are the quantifiable, cross-run comparable headline numbers.
type RunMetrics struct {
	TargetBPS       float64 `json:"target_bps"`
	ActualBPS       float64 `json:"actual_bps"`
	BitrateDeltaPct float64 `json:"bitrate_delta_pct"`
	QualityMean     float64 `json:"quality_mean"`
	QualityMin      float64 `json:"quality_min"`
	ManifestErrorMS float64 `json:"manifest_error_ms"`
	IndexRebuilt    int     `json:"index_rebuilt"`
}

// Manifest is the complete, deterministic output of one pipeline run. It
// contains no wall-clock fields so repeated runs over identical inputs are
// byte-identical and can be diffed directly.
type Manifest struct {
	SchemaVersion int              `json:"schema_version"`
	TimeBaseUS    uint32           `json:"unified_time_base_us"`
	Success       bool             `json:"batch_success"`
	RunID         string           `json:"run_id"` // content-derived, stable
	Metrics       RunMetrics       `json:"metrics"`
	Tracks        []TrackResult    `json:"tracks"`
	Errors        []*PipelineError `json:"errors"`
}

// ManifestFileName is the conventional batch manifest name.
const ManifestFileName = "manifest.json"

// AppendTrack registers one track result and rolls up batch-level errors.
func (m *Manifest) AppendTrack(r TrackResult) {
	m.Tracks = append(m.Tracks, r)
	m.Errors = append(m.Errors, r.Errors...)
}

// Finalize computes aggregate metrics and the batch success flag. A batch is
// only successful when no track has a fatal error; recoverable anomalies mark
// affected tracks as degraded but do not fail the whole batch.
func (m *Manifest) Finalize(targetBPS float64) {
	m.SchemaVersion = ManifestSchemaVersion
	m.TimeBaseUS = UnifiedTimeBase
	m.Success = true

	var totalBytes int64
	var maxDurationUS int64
	var qSum float64
	var qN int
	qMin := math.MaxFloat64
	var maxAlignUS int64
	for i := range m.Tracks {
		tr := &m.Tracks[i]
		if HasFatal(tr.Errors) {
			tr.Status = StatusFailed
			m.Success = false
		} else if len(tr.Errors) > 0 {
			if tr.Status != StatusFailed {
				tr.Status = StatusDegraded
			}
		} else if tr.Status == "" {
			tr.Status = StatusOK
		}
		if tr.Exec != nil {
			totalBytes += tr.Exec.Bytes
			qSum += tr.Exec.QualityMean * float64(tr.Exec.FrameCount)
			qN += tr.Exec.FrameCount
			if tr.Exec.QualityMin > 0 && float64(tr.Exec.QualityMin) < qMin {
				qMin = float64(tr.Exec.QualityMin)
			}
		}
		// Bitrate window includes the final frame's own duration: the
		// index duration spans first-to-last DTS, so one frame duration is
		// added back to cover the last sample.
		d := effectiveDurationUS(tr)
		if d > maxDurationUS {
			maxDurationUS = d
		}
		if tr.ClockStats.MaxErrorUS > maxAlignUS {
			maxAlignUS = tr.ClockStats.MaxErrorUS
		}
		if tr.IndexRebuilt {
			m.Metrics.IndexRebuilt++
		}
	}
	durationSec := float64(maxDurationUS) / float64(UnifiedTimeBase)
	if durationSec > 0 {
		m.Metrics.ActualBPS = float64(totalBytes*8) / durationSec
	}
	m.Metrics.TargetBPS = targetBPS
	if targetBPS > 0 {
		m.Metrics.BitrateDeltaPct = (m.Metrics.ActualBPS - targetBPS) / targetBPS * 100
	}
	if qN > 0 {
		m.Metrics.QualityMean = round2(qSum / float64(qN))
	}
	if qMin < math.MaxFloat64 {
		m.Metrics.QualityMin = qMin
	}
	m.Metrics.ManifestErrorMS = round3(float64(maxAlignUS) / 1000.0)
}

// Save writes the manifest atomically inside baseDir and returns its path
// relative resolution result for logging.
func (m *Manifest) Save(baseDir string) (string, error) {
	name, perr := ResolveLocal(baseDir, ManifestFileName)
	if perr != nil {
		return "", perr
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	if err := AtomicWriteFile(name, data, 0o644); err != nil {
		return "", err
	}
	return name, nil
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// effectiveDurationUS returns the bitrate window length for a track: the
// indexed span plus one final-frame duration, derived from the average frame
// spacing when fewer than two timestamps exist.
func effectiveDurationUS(tr *TrackResult) int64 {
	if tr.FrameCount <= 1 || tr.DurationUS <= 0 {
		return tr.DurationUS
	}
	avgStep := tr.DurationUS / int64(tr.FrameCount-1)
	return tr.DurationUS + avgStep
}
