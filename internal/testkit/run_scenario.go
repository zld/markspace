package testkit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"mediacore/internal/clock"
	"mediacore/internal/scan"
)

// scanDigest reads the input digest recorded against a manifest's sibling MP4.
func scanDigest(manifestPath string) (string, error) {
	mp4Path := filepath.Join(filepath.Dir(manifestPath), "track.mp4")
	m, _, err := scan.LoadManifest(mp4Path, manifestPath)
	if err != nil {
		return "", err
	}
	return m.DigestHex, nil
}

// seedBrokenCache writes a correctly-sized cache with a wrong trailer.
func seedBrokenCache(path string, count uint32) error {
	body := make([]byte, 64+int(count)*44)
	copy(body[0:8], "MCIDX001")
	trailer := make([]byte, 32)
	for i := range trailer {
		trailer[i] = 0xa5
	}
	return os.WriteFile(path, append(body, trailer...), 0o644)
}

func writeManifestFile(path string, m *scan.Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return clock.ErrInvariantf("testkit.manifest", "marshal: %v", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// RunScenario executes the bounded pipeline for one generated scenario. A
// corruption that produces a classified recoverable error is returned to
// the caller for reporting rather than being swallowed.
func RunScenario(sc Scenario, p DemoParams) (*PipelineResult, error) {
	pcfg := PipelineConfig{
		TargetBPS:       p.TargetBPS,
		MaxBatchSamples: 512,
		MaxBatchBytes:   p.BatchBytes,
		InFlightBytes:   2 << 20,
		SlowEvery:       p.SlowEvery,
	}
	var sleep func()
	if p.SlowEvery > 0 {
		sleep = func() { time.Sleep(2 * time.Millisecond) }
	}
	ctx := context.Background()

	// The no_keyframe fixture carries its full sync suppression in the
	// manifest itself, so it flows through the same pipeline as every other
	// scenario and fails at the keyframe-anchor lookup.
	return RunPipeline(ctx, sc.FixtureSet, pcfg, "", sleep)
}

// AcceptanceBounds captures the demo's numeric acceptance contract.
type AcceptanceBounds struct {
	MinQualitySamples int
	MaxDeviationPC    int64 // hundredths of a percent
	MaxAlignmentMS    int64
	MaxPeakBuffer     int64
	MaxDropped        uint64
}

// DefaultAcceptance matches the documented demo requirements.
func DefaultAcceptance() AcceptanceBounds {
	return AcceptanceBounds{
		MinQualitySamples: 10000,
		MaxDeviationPC:    200, // 2.00%
		MaxAlignmentMS:    2,
		MaxPeakBuffer:     2 << 20,
		MaxDropped:        0,
	}
}

// AssertAcceptance verifies the normal-run report. Any violation is an
// internal invariant failure (exit 4), since the generator calibrates every
// input to satisfy these bounds.
func AssertAcceptance(res *PipelineResult, b AcceptanceBounds) error {
	r := res.Report
	if r.QualitySamples <= b.MinQualitySamples {
		return clock.ErrInvariantf("demo.accept", "quality_samples=%d <= %d",
			r.QualitySamples, b.MinQualitySamples)
	}
	if r.DroppedSamples != b.MaxDropped {
		return clock.ErrInvariantf("demo.accept", "dropped_samples=%d want %d",
			r.DroppedSamples, b.MaxDropped)
	}
	if r.BitrateDeviationPC >= b.MaxDeviationPC {
		return clock.ErrInvariantf("demo.accept", "deviation hundredths=%d >= %d",
			r.BitrateDeviationPC, b.MaxDeviationPC)
	}
	if r.AlignmentErrorMS > b.MaxAlignmentMS {
		return clock.ErrInvariantf("demo.accept", "alignment_ms=%d > %d",
			r.AlignmentErrorMS, b.MaxAlignmentMS)
	}
	if r.PeakBufferBytes > b.MaxPeakBuffer {
		return clock.ErrInvariantf("demo.accept", "peak_buffer=%d > %d",
			r.PeakBufferBytes, b.MaxPeakBuffer)
	}
	if r.CacheState != "hit" && r.CacheState != "rebuilt" {
		return clock.ErrInvariantf("demo.accept", "unknown cache state %q", r.CacheState)
	}
	return nil
}
