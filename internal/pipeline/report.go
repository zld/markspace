package pipeline

import (
	"math"

	"github.com/archive/mediakernel/internal/manifest"
	"github.com/archive/mediakernel/internal/rtprecover"
)

// Stats holds every quality metric printed to the terminal and embedded in the
// JSON report. All numeric fields are always finite: bitrate errors are
// guarded against zero targets, and quality is a bounded score in [0,100].
type Stats struct {
	InputDigest       string  `json:"input_digest"`
	IndexRebuilt      bool    `json:"index_rebuilt"`
	PacketsReceived   int     `json:"packets_received"`
	PacketsEmitted    int     `json:"packets_emitted"`
	PacketsDuplicate  int     `json:"packets_duplicate"`
	PacketsLate       int     `json:"packets_late"`
	LossBursts        int     `json:"loss_bursts"`
	Segments          int     `json:"segments"`
	SilenceMS         int64   `json:"silence_ms"`
	DedupHits         int     `json:"dedup_hits"`
	TargetBitrateBps  int64   `json:"target_bitrate_bps"`
	ActualBitrateBps  int64   `json:"actual_bitrate_bps"`
	BitrateErrorPct   float64 `json:"bitrate_error_pct"`
	QualityScore      float64 `json:"quality_score"`
	ManifestAlignMS   int64   `json:"manifest_alignment_ms"`
	RecoveredSegments int     `json:"recovered_segments"`
}

// fillTransport copies window-level counters into the report.
func (s *Stats) fillTransport(rs rtprecover.Stats) {
	s.PacketsReceived = rs.Received
	s.PacketsEmitted = rs.Emitted + rs.Flushed
	s.PacketsDuplicate = rs.Duplicate
	s.PacketsLate = rs.Late
}

// buildStats computes all derived quality metrics from the result manifest.
func buildStats(result *manifest.ResultManifest, in *Input,
	cfg Config, maxBoundaryDev, resumedBoundary int64) *Stats {

	s := &Stats{
		InputDigest:      result.InputDigest,
		IndexRebuilt:     result.IndexRebuilt,
		LossBursts:       len(result.Gaps),
		Segments:         len(result.Segments),
		TargetBitrateBps: cfg.TargetBitrate,
		ManifestAlignMS:  maxBoundaryDev / 1000,
	}

	var silenceUS int64
	for _, si := range result.Silences {
		if si.Status == manifest.StatusSilent {
			silenceUS += si.EndUS - si.StartUS
		}
	}
	s.SilenceMS = silenceUS / 1000

	for _, fp := range result.Fingerprints {
		if fp.Duplicate {
			// A dedup "hit" is every additional source range beyond the first.
			s.DedupHits += len(fp.SourceRanges) - 1
		}
	}

	// Actual bitrate from recovered payload bytes over presented duration.
	var totalBytes int64
	for _, seg := range result.Segments {
		totalBytes += seg.Bytes
	}
	durationUS := spanUS(result.Segments)
	if durationUS > 0 {
		// bits per second = bytes*8 / seconds.
		s.ActualBitrateBps = int64(math.Round(float64(totalBytes*8) / (float64(durationUS) / 1_000_000)))
	}
	errPct, _ := rtprecover.BitrateError(cfg.TargetBitrate, s.ActualBitrateBps)
	s.BitrateErrorPct = errPct

	s.QualityScore = qualityScore(result, s)

	// Recovered segments are those already completed before the resume point.
	if resumedBoundary > 0 {
		for _, seg := range result.Segments {
			if seg.EndUS <= resumedBoundary {
				s.RecoveredSegments++
			}
		}
	}
	return s
}

// spanUS returns the covered timeline span of the segments.
func spanUS(segs []manifest.Segment) int64 {
	if len(segs) == 0 {
		return 0
	}
	return segs[len(segs)-1].EndUS - segs[0].StartUS
}

// qualityScore combines delivery integrity, alignment and bitrate adherence
// into one bounded, monotonic score. It is always finite and within [0,100].
func qualityScore(result *manifest.ResultManifest, s *Stats) float64 {
	// 40% delivery (no gaps, no late loss), 30% alignment, 30% bitrate fidelity.
	delivery := 100.0
	if s.PacketsReceived > 0 {
		missing := 0
		for _, g := range result.Gaps {
			missing += g.MissingCount
		}
		lossRatio := float64(missing+s.PacketsLate) / float64(s.PacketsReceived)
		delivery = 100 * (1 - clamp01(lossRatio))
	}
	alignment := 100.0
	if s.ManifestAlignMS > 40 {
		alignment = 100 * 40 / float64(s.ManifestAlignMS)
	}
	bitrateFidelity := 100 - clamp01(math.Abs(s.BitrateErrorPct)/100)*100
	score := 0.4*delivery + 0.3*alignment + 0.3*bitrateFidelity
	if math.IsNaN(score) || math.IsInf(score, 0) {
		return 0
	}
	return math.Round(clamp01(score/100)*10000) / 100
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}
