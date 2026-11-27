// Package pipeline: the five-stage execution path.
package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

	"example.com/assetpack/internal/fault"
	"example.com/assetpack/internal/manifest"
	"example.com/assetpack/internal/migrate"
	"example.com/assetpack/internal/rollback"
)

// Run executes the migration and always returns a populated outcome, even
// when processing fails part-way.
func Run(opts Options) (*Outcome, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	for _, dir := range []string{opts.WorkDir, opts.ReleaseDir, opts.StateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fault.New(fault.StageCommit, fault.KindIO, "create %s: %v", dir, err)
		}
	}
	cp, err := rollback.New(opts.ReleaseDir)
	if err != nil {
		return nil, err
	}
	// Classify any interrupted previous run before starting new work.
	recovered, err := cp.Recover()
	if err != nil {
		return nil, err
	}
	committed, err := cp.ReadManifest()
	if err != nil {
		return nil, err
	}
	// Keep the previous journal for reference, then start this attempt from
	// a clean pending journal.  Statuses therefore always describe the run
	// being reported, never a stale mix of earlier attempts.
	previous, err := migrate.LoadJournal(opts.StateDir, opts.Release)
	if err != nil {
		return nil, err
	}
	journal := migrate.NewJournal(opts.Release)
	out := &Outcome{Journal: journal, Faults: &fault.List{}, Before: committed,
		Recovered: recovered, Previous: previous}

	candidate, assets, buildErr := buildAssets(opts, journal)
	out.Assets = assets
	out.Resume = resumeFrom(assets)
	if candidate != nil {
		out.Faults.Merge(candidate.Faults)
	}
	if buildErr != nil {
		return failOutcome(opts, cp, journal, out, buildErr)
	}

	// Stage 1 and 2 are complete once every asset has been parsed and
	// normalized.  Record the resume boundary each stage reached.
	journal.Set(migrate.StageParse, migrate.StatusDone, out.Resume.Offset, out.Resume.Sample,
		fmt.Sprintf("%d assets parsed", len(assets)), nil)
	journal.Set(migrate.StageAudio, migrate.StatusDone, out.Resume.Offset, totalOutFrames(assets),
		fmt.Sprintf("%d assets normalized", len(assets)), nil)
	if err := journal.Persist(opts.StateDir); err != nil {
		return out, err
	}
	if err := maybeInject(opts, migrate.StageAudio); err != nil {
		return failOutcome(opts, cp, journal, out, err)
	}

	// Manifest stage: convert, merge and stage the complete candidate.
	artifacts := artifactsOf(assets)
	merged, diff, err := prepareManifest(opts, committed, cp, candidate, assets)
	if err != nil {
		return failOutcome(opts, cp, journal, out, err)
	}
	out.Diff = diff
	stage, err := cp.Stage(merged, artifacts)
	if err != nil {
		return failOutcome(opts, cp, journal, out, err)
	}
	journal.Set(migrate.StageStageManifest, migrate.StatusDone, out.Resume.Offset, out.Resume.Sample,
		fmt.Sprintf("added=%d reused=%d replaced=%d", diff.Added, diff.Reused, diff.Replaced), nil)
	if err := journal.Persist(opts.StateDir); err != nil {
		return out, err
	}
	if err := maybeInject(opts, migrate.StageStageManifest); err != nil {
		return failOutcome(opts, cp, journal, out, err)
	}

	// Verify stage: re-hash every staged artifact against the manifest.
	if err := verifyStaged(stage, merged, artifacts); err != nil {
		return failOutcome(opts, cp, journal, out, err)
	}
	journal.Set(migrate.StageVerify, migrate.StatusDone, -1, totalOutFrames(assets),
		"staged artifacts hashed and matched", nil)
	if err := journal.Persist(opts.StateDir); err != nil {
		return out, err
	}
	if err := maybeInject(opts, migrate.StageVerify); err != nil {
		return failOutcome(opts, cp, journal, out, err)
	}

	// Commit stage.
	if err := cp.Commit(stage); err != nil {
		return failOutcome(opts, cp, journal, out, err)
	}
	journal.Set(migrate.StageCommit, migrate.StatusDone, -1, -1, "committed", nil)
	if err := journal.Persist(opts.StateDir); err != nil {
		return out, err
	}
	if err := maybeInject(opts, migrate.StageCommit); err != nil {
		// The commit point has passed: the release is already the new one.
		// Report the injected error but leave the committed state intact.
		// The rename is the commit point: the new release is durable even
		// though the post-commit work was interrupted.
		out.Failure = asFault(err)
		out.Committed = true
		out.RunOK = false
		out.Valid = cp.VerifyDir() == nil
		out.ManifestSHA = merged.Digest
		out.After = merged
		return out, err
	}

	after, err := cp.ReadManifest()
	if err != nil {
		return out, err
	}
	out.After = after
	out.Committed = true
	if after != nil {
		out.ManifestSHA = after.Digest
	}
	if err := cp.VerifyDir(); err != nil {
		out.Faults.Add(asFault(err))
		out.Valid = false
		out.RunOK = false
		return out, err
	}
	out.Valid = true
	out.RunOK = true
	return out, nil
}

// resumeFrom finds the last complete boundary across all assets: the end of
// the last trusted transport packet that contributed audio.
func resumeFrom(assets []AssetResult) migrate.Position {
	var pos migrate.Position
	for _, a := range assets {
		pos.Offset = maxI64(pos.Offset, a.ParseOffset+1)
		pos.Sample += a.ParseSample
	}
	return pos
}

// verifyStaged re-hashes the bytes actually written to the staging directory
// and compares them with both the manifest and the in-memory containers.
func verifyStaged(stage string, m *manifest.V2, artifacts map[string][]byte) error {
	digests := make(map[string]string, len(artifacts))
	for name, data := range artifacts {
		got, _, err := rollback.HashFile(filepath.Join(stage, rollback.ArtifactDir, name))
		if err != nil {
			return err
		}
		want := sha256Hex(data)
		if got != want {
			return fault.New(fault.StageVerify, fault.KindDigest,
				"staged artifact %q hashes to %s, produced %s", name, got, want)
		}
		digests[name] = got
	}
	return m.Verify(digests)
}

func totalOutFrames(assets []AssetResult) int64 {
	var n int64
	for _, a := range assets {
		n += a.Report.OutFrames
	}
	return n
}

func artifactsOf(assets []AssetResult) map[string][]byte {
	out := make(map[string][]byte, len(assets))
	for _, a := range assets {
		out[a.Artifact] = a.Container
	}
	return out
}
