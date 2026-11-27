package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"

	"example.com/continuity/internal/checkpoint"
	"example.com/continuity/internal/fault"
	"example.com/continuity/internal/manifest"
	"example.com/continuity/internal/media"
	"example.com/continuity/internal/provenance"
)

// Acceptance thresholds from the continuity specification.
const (
	maxBitrateErrorPct = 5.0
	minPSNRDB          = 40.0
	minSSIM            = 0.98
	maxAlignNS         = int64(1)
)

// VerifyReport summarizes an acceptance verification.
type VerifyReport struct {
	FrameCount       int
	SummaryIdentical bool
	AllManifestsSame bool
	ThresholdsMet    bool
}

// Verify re-runs the deterministic pipeline against freshly regenerated
// inputs and checks the stored output byte-for-byte, then validates every
// acceptance threshold. A mismatch returns a structured verify fault.
func (e *Engine) Verify(ctx context.Context) (*VerifyReport, error) {
	// 1. The previously committed summary must exist.
	storedSummary, err := os.ReadFile(filepath.Join(e.outDir, manifest.SummaryFile))
	if err != nil {
		return nil, fault.New(fault.StageVerify, "read-summary", err)
	}

	// 2. Regenerate the identical synthetic inputs and rerun into a temp dir.
	spec := media.DefaultSpec()
	frameData, err := spec.WriteFrameStream()
	if err != nil {
		return nil, err
	}
	rtpData, err := spec.WriteRTPStream()
	if err != nil {
		return nil, err
	}
	tmpDir, err := os.MkdirTemp("", "continuity-verify-")
	if err != nil {
		return nil, fault.New(fault.StageVerify, "tempdir", err)
	}
	defer os.RemoveAll(tmpDir)

	rerun := New(DefaultConfig(), tmpDir)
	res, err := rerun.Run(ctx, frameData, rtpData)
	if err != nil {
		return nil, err
	}
	freshSummary, err := os.ReadFile(filepath.Join(tmpDir, manifest.SummaryFile))
	if err != nil {
		return nil, err
	}

	report := &VerifyReport{FrameCount: res.Summary.Frames}
	report.SummaryIdentical = bytes.Equal(storedSummary, freshSummary)

	// 3. Every manifest must be byte-identical across runs.
	allSame := report.SummaryIdentical
	for _, name := range []string{
		manifest.SegmentsFile, manifest.SilenceFile, manifest.DedupFile,
	} {
		a, errA := os.ReadFile(filepath.Join(e.outDir, name))
		b, errB := os.ReadFile(filepath.Join(tmpDir, name))
		if errA != nil || errB != nil || !bytes.Equal(a, b) {
			allSame = false
		}
	}
	report.AllManifestsSame = allSame

	// 4. Threshold checks against the stored summary.
	stored, _, err := manifest.ReadSummary(e.outDir)
	if err != nil {
		return nil, err
	}
	report.ThresholdsMet = stored.Frames == spec.Frames &&
		stored.BitrateErrorPct <= maxBitrateErrorPct &&
		stored.PSNRDB >= minPSNRDB &&
		stored.SSIM >= minSSIM &&
		stored.ManifestAlignNS <= maxAlignNS

	// 5. Provenance and checkpoint digest integrity.
	trace, err := provenance.Read(e.outDir)
	if err != nil {
		return nil, err
	}
	if err := trace.Verify(); err != nil {
		return nil, err
	}
	if e.store.Exists() {
		inputDigest := checkpoint.DigestOf(frameData)
		configDigest, err := provenance.ConfigDigestOf(e.cfg)
		if err != nil {
			return nil, err
		}
		if _, err := e.store.Load(inputDigest, configDigest); err != nil {
			return nil, err
		}
	}

	if !report.SummaryIdentical || !report.AllManifestsSame {
		return report, fault.New(fault.StageVerify, "byte-compare",
			errors.New("manifest bytes differ between runs"))
	}
	if !report.ThresholdsMet {
		return report, fault.New(fault.StageVerify, "threshold",
			errors.New("acceptance thresholds not met"))
	}
	return report, nil
}
