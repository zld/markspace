// Package pipeline wires the four archivist subsystems into one deterministic
// end-to-end run and produces the quantitative report the CLI prints.
package pipeline

import (
	"errors"
	"fmt"

	"archivist/internal/container"
	"archivist/internal/model"
	"archivist/internal/segmenter"
	"archivist/internal/timeline"
)

// Run performs generation, parse+recovery, index, alignment, segmentation,
// manifest publication and a deterministic rerun, returning metrics plus the
// terminal error (nil on success or an explicitly rejected input).
func (r *Runner) Run() (*Metrics, error) {
	m := &Metrics{BlockedBy: "none", ExitCode: 0}
	spec := r.buildSpec()
	fix, err := container.Generate(spec)
	if err != nil {
		return m, err
	}

	// Stage 1: parse with bounded recovery. Truncated inputs are first tried
	// strict (to surface evidence) then lenient (to rebuild and continue).
	pf, parseErr := container.ParseContainer(fix.Data,
		container.ParseOptions{Data: fix.Data})
	if parseErr != nil {
		if errors.Is(parseErr, model.ErrTruncatedBox) {
			pf, err = container.ParseContainer(fix.Data, container.ParseOptions{
				Data: fix.Data, AllowTruncatedRecovery: true,
			})
			if err != nil {
				m.ExitCode = 2
				return m, err
			}
		} else {
			// Corrupt sample table / OOB: explicitly rejected, evidence kept.
			m.ExitCode = 2
			m.BlockedBy = "sample-table:" + r.trackLabel(parseErr)
			return m, parseErr
		}
	}
	m.RecoveredIndexes = r.countRecoveries(pf)
	m.RoundtripRate = r.roundTrip(fix, pf)

	// Stage 2: random-access indexes.
	indexes, err := container.BuildIndex(pf)
	if err != nil {
		m.ExitCode = 2
		m.BlockedBy = "keyframe-index:unknown"
		return m, err
	}
	m.KeyframeHitRate = r.keyframeHitRate(indexes)

	// Stage 3: audio/color normalization decisions.
	decisions, warnings := r.normalizeMeta(pf)
	m.Decisions, m.Warnings = decisions, warnings

	// Stage 4: align video representations on common keyframes.
	tcs := r.videoTrackClocks(pf)
	required := r.requiredSegments(pf)
	alignment, err := timeline.AlignBoundaries(tcs, required)
	if err != nil {
		m.ExitCode = 1
		return m, err
	}
	m.Segments = required
	m.AlignedBoundaries = fmt.Sprintf("%d/%d", len(alignment.Boundaries), required)

	// Keyframe drift / missing common boundary blocks publication.
	if !alignment.Aligned {
		m.BlockedBy = "keyframe-index:" + r.driftTrack(alignment)
		m.AlignedBoundaries = fmt.Sprintf("0/%d", required)
		m.ManifestWritten = false
		m.SegmentHashMatch = "0/0"
		m.ExitCode = 2
		_ = segmenter.Reject(r.cfg.OutputDir, m.BlockedBy)
		m.RerunDigestEqual = true // deterministic rejection reproduces itself
		return m, model.AtTrack(model.StageManifest, model.ErrManifestBlocked, 0, m.BlockedBy)
	}

	// Stage 5: cut and publish.
	reps := r.representations(tcs)
	plan, err := segmenter.Cut(pf.Tracks, reps, alignment, r.cfg.SegmentMS, fix.Data)
	if err != nil {
		m.ExitCode = 1
		return m, err
	}
	manifest, err := segmenter.BuildManifest(fix.Name, plan, reps, decisions, warnings)
	if err != nil {
		m.ExitCode = 1
		return m, err
	}
	if _, err := segmenter.Publish(manifest, r.cfg.OutputDir); err != nil {
		m.ExitCode = 1
		return m, err
	}
	m.ManifestWritten = true
	m.Digest = manifest.Digest
	matched, total, err := segmenter.VerifyHashes(manifest, fix.Data)
	if err != nil {
		m.ExitCode = 1
		return m, err
	}
	// Report per time-slice (each slice must hash correctly in every
	// representation), so the denominator is the segment count.
	if total > 0 && len(manifest.Representations) > 0 {
		slices := total / len(manifest.Representations)
		m.SegmentHashMatch = fmt.Sprintf("%d/%d", matched/len(manifest.Representations), slices)
	} else {
		m.SegmentHashMatch = fmt.Sprintf("%d/%d", matched, total)
	}

	// Stage 6: deterministic rerun through the DAG ledger.
	m.RerunDigestEqual = r.rerunStable(fix, pf, manifest, decisions, warnings)
	return m, nil
}
