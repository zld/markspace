// Package report verifies cross-stage invariants and emits a machine-readable
// summary of a media processing run. It is the single place where recovery,
// synthesis, bitrate and rendering results are reconciled, so an incident
// replay can be checked against stable field names.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"

	"github.com/medialab/medialab/internal/clock"
	"github.com/medialab/medialab/internal/render"
	"github.com/medialab/medialab/internal/rtprecover"
)

// Metrics carries the counters each stage hands to the reporter.
type Metrics struct {
	TargetBps        int64
	ActualBps        int64
	BitrateDeltaPct  int
	Frames           int
	Qualities        []float64
	AlignmentErrMs   int64
	Duplicates       uint64
	LateDropped      uint64
	ChecksumBad      uint64
	Gaps             []rtprecover.Gap
	PeakQueue        int
	TimelineOverflow int
	FailedStage      string
}

// Summary is the stable JSON contract consumed by operators and diff tooling.
type Summary struct {
	TargetBps         int64   `json:"target_bps"`
	ActualBps         int64   `json:"actual_bps"`
	BitrateDeltaPct   int     `json:"bitrate_delta_pct"`
	Frames            int     `json:"frames"`
	QualityMean       float64 `json:"quality_mean"`
	QualityP95        float64 `json:"quality_p95"`
	AlignmentErrorMs  int64   `json:"alignment_error_ms"`
	Duplicates        uint64  `json:"duplicates"`
	LateDropped       uint64  `json:"late_dropped"`
	ChecksumBad       uint64  `json:"checksum_bad"`
	Gaps              int     `json:"gaps"`
	PeakQueue         int     `json:"peak_queue"`
	TimelineOverflow  int     `json:"timeline_overflow"`
	FrameSHA256       string  `json:"frame_sha256"`
	FailedStage       string  `json:"failed_stage,omitempty"`
}

// Verify enforces the cross-stage invariants. A violation is returned as a
// clock.MediaError with the offending stage and position rather than being
// silently swallowed.
func Verify(m Metrics, frames []render.Frame) error {
	if m.AlignmentErrMs > 1 {
		return clock.NewError(clock.KindNonMonotonic, "report", "", 0,
			fmt.Sprintf("cross-track alignment %dms exceeds 1ms budget", m.AlignmentErrMs))
	}
	if m.TimelineOverflow > 0 {
		return clock.NewError(clock.KindOverflow, "report", "", 0,
			fmt.Sprintf("%d timeline overflows observed", m.TimelineOverflow))
	}
	var prevMs int64 = math.MinInt64
	for _, f := range frames {
		if f.ManifestMs <= prevMs {
			return clock.NewError(clock.KindNonMonotonic, "render", f.TrackID,
				f.ManifestMs, "exported frames are not monotonic in manifest time")
		}
		prevMs = f.ManifestMs
		if f.SHA256 == "" {
			return clock.NewError(clock.KindRenderFailure, "report", f.TrackID,
				f.ManifestMs, "frame missing SHA-256")
		}
	}
	return nil
}

// Build assembles the summary, folding frame hashes into one digest that is
// stable across repeated runs of the same input.
func Build(m Metrics, frames []render.Frame) Summary {
	s := Summary{
		TargetBps:        m.TargetBps,
		ActualBps:        m.ActualBps,
		BitrateDeltaPct:  m.BitrateDeltaPct,
		Frames:           m.Frames,
		QualityMean:      mean(m.Qualities),
		QualityP95:       percentile(m.Qualities, 95),
		AlignmentErrorMs: m.AlignmentErrMs,
		Duplicates:       m.Duplicates,
		LateDropped:      m.LateDropped,
		ChecksumBad:      m.ChecksumBad,
		Gaps:             len(m.Gaps),
		PeakQueue:        m.PeakQueue,
		TimelineOverflow: m.TimelineOverflow,
		FrameSHA256:      foldFrameHashes(frames),
		FailedStage:      m.FailedStage,
	}
	return s
}

// WriteJSON persists the summary with stable field ordering (struct order).
func WriteJSON(path string, s Summary) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return clock.NewError(clock.KindRenderFailure, "report", "", 0, err.Error())
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return clock.NewError(clock.KindRenderFailure, "report", "", 0, err.Error())
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return clock.NewError(clock.KindRenderFailure, "report", "", 0, err.Error())
	}
	return nil
}

// foldFrameHashes chains per-frame SHA-256 values in timeline order and
// hashes the concatenation, so any frame change or reordering changes the
// digest. Frames are required to arrive in manifest order by Verify.
func foldFrameHashes(frames []render.Frame) string {
	h := sha256.New()
	for _, f := range frames {
		h.Write([]byte(f.SHA256))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	return round2(sum / float64(len(xs)))
}

// percentile returns the nearest-rank p-th percentile (0 < p <= 100).
func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := append([]float64(nil), xs...)
	sort.Float64s(cp)
	rank := int(math.Ceil(p / 100 * float64(len(cp))))
	if rank < 1 {
		rank = 1
	}
	return round2(cp[rank-1])
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
