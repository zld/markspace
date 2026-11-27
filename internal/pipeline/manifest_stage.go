// Package pipeline: converting the legacy manifest and staging the release.
package pipeline

import (
	"os"
	"path/filepath"

	"example.com/assetpack/internal/fault"
	"example.com/assetpack/internal/manifest"
	"example.com/assetpack/internal/rollback"
)

// prepareManifest converts the legacy manifest, merges it with whatever is
// already committed, and stages the complete candidate release.
func prepareManifest(opts Options, committed *manifest.V2, cp *rollback.Checkpoint,
	candidate *Candidate, assets []AssetResult) (*manifest.V2, manifest.DiffCounts, error) {
	adapter := &manifest.Adapter{Release: opts.Release, Target: manifest.TargetSpec{
		Rate: opts.Target.Format.Rate, Layout: string(opts.Target.Format.Layout), LUFS: opts.Target.Loudness,
	}}
	// The legacy manifest is read again here so a digest disagreement is
	// detected against the bytes on disk, not a cached copy.
	legacy, err := readLegacy(opts)
	if err != nil {
		return nil, manifest.DiffCounts{}, err
	}
	if legacy.Release != candidate.Legacy.Release {
		return nil, manifest.DiffCounts{}, fault.New(fault.StageManifest, fault.KindInvalidArg,
			"legacy manifest release changed from %q to %q", candidate.Legacy.Release, legacy.Release)
	}
	fresh, err := adapter.Convert(legacy, resolvedFrom(assets, legacy))
	if err != nil {
		return nil, manifest.DiffCounts{}, err
	}
	merged, diff, err := manifest.Merge(committed, fresh)
	if err != nil {
		return nil, manifest.DiffCounts{}, err
	}
	// Note: the caller stages the merged manifest.  Convert only builds it.
	_ = fresh
	return merged, diff, nil
}

func readLegacy(opts Options) (*manifest.V1, error) {
	f, err := os.Open(filepath.Join(opts.InputDir, "manifest.v1.json"))
	if err != nil {
		return nil, fault.New(fault.StageManifest, fault.KindIO, "open legacy manifest: %v", err)
	}
	defer f.Close()
	legacy, _, err := (&manifest.Adapter{}).ReadFile(f)
	if err != nil {
		return nil, err
	}
	if legacy == nil {
		return nil, fault.New(fault.StageManifest, fault.KindUnknownVer,
			"input manifest is not a legacy v1 manifest")
	}
	return legacy, nil
}
