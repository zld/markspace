package pipeline

import (
	"fmt"
	"math"

	"regional.example/mediad/internal/audio"
	"regional.example/mediad/internal/clock"
	"regional.example/mediad/internal/manifest"
	"regional.example/mediad/internal/rtp"
)

// Metric is one acceptance figure with its bound and verdict.
type Metric struct {
	// Name is the field printed by the CLI.
	Name string
	// Value is formatted for display.
	Value string
	// Passed reports whether the metric satisfies its bound.
	Passed bool
	// Detail explains a failure.
	Detail string
}

// QualityReport holds every acceptance metric in a stable order.
type QualityReport struct {
	Metrics []Metric
}

// Add appends a metric.
func (q *QualityReport) Add(m Metric) { q.Metrics = append(q.Metrics, m) }

// Failed returns the metric names that did not pass.
func (q *QualityReport) Failed() []string {
	var out []string
	for _, m := range q.Metrics {
		if !m.Passed {
			out = append(out, m.Name+" ("+m.Detail+")")
		}
	}
	return out
}

// FailedNames only lists names.
func (q *QualityReport) FailedNames() []string {
	var out []string
	for _, m := range q.Metrics {
		if !m.Passed {
			out = append(out, m.Name)
		}
	}
	return out
}

// Metric values from a completed pipeline.
type metricInput struct {
	TargetKbps          int
	ActualKbps          float64
	DeviationPct        float64
	SampleRate          int
	Layout              string
	Loudness            audio.LoudnessResult
	TargetLUFS          float64
	TruePeakDBTP        float64
	CeilingDBTP         float64
	ClippedSamples      int64
	TimelineErrorTicks  int64
	RecoveredSamples    int64
	MissingDurationMS   int64
	ManifestAlignmentMS int64
	// DataLoss means confirmed media was actually lost (truncation or a
	// damaged sample), so a non-zero missing duration is required.  A rebuilt
	// index with no lost media does not set it.
	DataLoss bool
	// RecoveredSamplesExpected is set when some structural recovery ran, so a
	// positive recovered-sample count is required.
	RecoveredSamplesExpected bool
	OutputSHA256             string
	OutputPath               string
}

// BuildQuality applies the acceptance bounds to a finished run, as defined by
// the operator contract.  These checks live here rather than in the CLI so
// tests exercise the exact same thresholds the demo uses.
func BuildQuality(m metricInput) QualityReport {
	q := QualityReport{}
	q.Add(Metric{
		Name: "actual_kbps", Value: fmt.Sprintf("%.1f", m.ActualKbps),
		Passed: m.DeviationPct <= 5,
		Detail: fmt.Sprintf("deviation %.2f%% from target %d kbps", m.DeviationPct, m.TargetKbps),
	})
	q.Add(Metric{
		Name: "sample_rate", Value: fmt.Sprintf("%d", m.SampleRate),
		Passed: m.SampleRate == 48000,
		Detail: fmt.Sprintf("output rate is %d, want 48000", m.SampleRate),
	})
	q.Add(Metric{
		Name: "layout", Value: m.Layout,
		Passed: validLayout(m.Layout),
		Detail: fmt.Sprintf("layout %q is not supported", m.Layout),
	})
	lufsDev := math.Abs(m.Loudness.IntegratedLUFS - m.TargetLUFS)
	lufsOK := lufsDev <= 1.5 || m.Loudness.Silent
	detail := fmt.Sprintf("integrated %.2f LUFS vs target %.2f, deviation %.2f", m.Loudness.IntegratedLUFS, m.TargetLUFS, lufsDev)
	if m.Loudness.Silent {
		lufsOK = true
		detail = "input was silent; loudness target cannot be met and is reported as silent"
	}
	lufsValue := fmt.Sprintf("%.2f", m.Loudness.IntegratedLUFS)
	if m.Loudness.Silent {
		lufsValue = "silent"
	}
	q.Add(Metric{Name: "integrated_lufs", Value: lufsValue, Passed: lufsOK, Detail: detail})
	q.Add(Metric{
		Name: "true_peak_dbtp", Value: fmt.Sprintf("%.2f", m.TruePeakDBTP),
		Passed: m.TruePeakDBTP <= m.CeilingDBTP+1e-9,
		Detail: fmt.Sprintf("true peak %.2f exceeds ceiling %.2f", m.TruePeakDBTP, m.CeilingDBTP),
	})
	// Clipping is only a failure on a clean, undamaged asset.
	clipOK := m.ClippedSamples == 0 || m.DataLoss
	q.Add(Metric{
		Name: "quality_clipped_samples", Value: fmt.Sprintf("%d", m.ClippedSamples),
		Passed: clipOK,
		Detail: fmt.Sprintf("%d clipped samples on an undamaged asset", m.ClippedSamples),
	})
	q.Add(Metric{
		Name: "timeline_error_ticks", Value: fmt.Sprintf("%d", m.TimelineErrorTicks),
		Passed: m.TimelineErrorTicks >= 0 && m.TimelineErrorTicks <= 1,
		Detail: fmt.Sprintf("timeline error %d ticks exceeds 1", m.TimelineErrorTicks),
	})
	// Recovered samples must be positive whenever recovery actually salvaged
	// data (index rebuild or truncation).  Missing duration is only required
	// when media was genuinely lost; a rebuilt index that loses nothing has a
	// legitimate zero.
	recoveredRequired := m.RecoveredSamplesExpected
	q.Add(Metric{
		Name: "recovered_samples", Value: fmt.Sprintf("%d", m.RecoveredSamples),
		Passed: !recoveredRequired || m.RecoveredSamples > 0,
		Detail: "recovery ran but produced no samples",
	})
	q.Add(Metric{
		Name: "missing_duration_ms", Value: fmt.Sprintf("%d", m.MissingDurationMS),
		Passed: !m.DataLoss || m.MissingDurationMS > 0,
		Detail: "media was lost but no duration was reported missing",
	})
	q.Add(Metric{
		Name: "manifest_alignment_ms", Value: fmt.Sprintf("%d", m.ManifestAlignmentMS),
		Passed: m.ManifestAlignmentMS >= 0 && m.ManifestAlignmentMS <= 1,
		Detail: fmt.Sprintf("alignment %d ms exceeds 1", m.ManifestAlignmentMS),
	})
	q.Add(Metric{
		Name: "target_kbps", Value: fmt.Sprintf("%d", m.TargetKbps),
		Passed: m.TargetKbps > 0,
		Detail: "target bitrate must be positive",
	})
	q.Add(Metric{
		Name: "output_sha256", Value: m.OutputSHA256,
		Passed: len(m.OutputSHA256) == 64,
		Detail: "output hash is not a full SHA-256 digest",
	})
	return q
}

func validLayout(l string) bool {
	switch l {
	case "mono", "stereo", "5.1":
		return true
	default:
		return false
	}
}

// rtpStatsFrom converts an RTP summary to manifest statistics.
func rtpStatsFrom(s *rtp.Summary, target int) manifest.PacketStats {
	_, actual, dev := s.KPSSummary(target)
	return manifest.PacketStats{
		Packets:        len(s.Packets),
		TotalBytes:     s.TotalBytes,
		TotalFrames:    s.TotalFrames,
		GapsBefore:     s.GapsBefore,
		NonMonotonic:   s.NonMonotonic,
		MediaTicksMS:   ticksToMS(s.MediaTicks, clock.Rate48000),
		MissingTicksMS: ticksToMS(s.MissingTicks, clock.Rate48000),
		ActualKbps:     actual,
		TargetKbps:     target,
		DeviationPct:   dev,
	}
}

func ticksToMS(ticks int64, r clock.Rate) int64 {
	ms, err := clock.TicksToMilliseconds(ticks, r)
	if err != nil {
		return 0
	}
	return ms
}
