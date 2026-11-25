package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"

	"replaycheck/internal/manifest"
	"replaycheck/internal/model"
	"replaycheck/internal/report"
)

func init() { register(model.StageManifest, (*run).stageManifest) }

func (r *run) stageManifest() error {
	input := model.Fingerprint("manifest", r.deriveOut, r.eventAsset.Digest)
	var m manifest.Manifest
	if out, ok := r.fetchIfSkipped(model.StageManifest, "manifest", input, &m); ok {
		r.man = &m
		r.manifestOut = out
		r.record(model.StageManifest, input, true)
		r.fillResult()
		r.finish(model.StageManifest, out)
		return nil
	}
	cached := r.fetch("manifest", input, &m)
	if !cached {
		if err := r.ensureInputsForManifest(); err != nil {
			return err
		}
		refs := make([]manifest.ClipRef, 0, len(r.clips))
		for _, c := range r.clips {
			refs = append(refs, manifest.ClipRef{Segment: c.Segment, Digest: c.Digest, Bytes: c.Bytes})
		}
		built, err := manifest.Build(string(r.cfg.Kind), r.tl, r.plan, r.pres, refs, r.eventAsset.Digest)
		if err != nil {
			return err
		}
		m = *built
		if err := r.save("manifest", input, built); err != nil {
			return err
		}
	}
	r.man = &m
	r.manifestOut, _ = model.DigestJSON(m)
	r.record(model.StageManifest, input, cached)
	if err := r.emitManifest(); err != nil {
		return err
	}
	r.finish(model.StageManifest, r.manifestOut)
	return nil
}

func (r *run) ensureInputsForManifest() error {
	if r.tl == nil {
		if err := r.stageNormalize(); err != nil {
			return err
		}
	}
	if r.plan == nil {
		if err := r.stageSplit(); err != nil {
			return err
		}
	}
	if r.pres == nil {
		if err := r.stageDerive(); err != nil {
			return err
		}
	}
	return nil
}

// emitManifest publishes the manifest, verifies it against the stored
// artifacts and reports field-level differences from any previous run.
func (r *run) emitManifest() error {
	path := filepath.Join(r.siteDir, "manifest.json")
	diff := manifest.Diff{Same: true}
	if prev, err := readManifest(path); err == nil {
		diff = manifest.Compare(prev, r.man)
	}
	polDigest, _ := r.policyDigest()
	rep, verr := manifest.Verify(r.man, r.mediaAsset.Digest, polDigest, r.cfg.Budget.MinQuality, r.blobs.Recompute)
	r.out.Emit(report.KindVerify, rep)
	r.result.DiffCount = diff.Count
	if diff.Count > 0 {
		r.out.Emit(report.KindDiff, diff)
	}
	r.fillResult()
	if verr != nil {
		return verr
	}
	return writeManifest(path, r.man)
}

func (r *run) fillResult() {
	r.result.ManifestDigest = r.man.Digest
	r.result.ManifestPath = filepath.Join(r.siteDir, "manifest.json")
	r.result.TotalBytes = r.man.TotalBytes
	r.result.Segments = len(r.man.Segments)
	r.result.MinQualityPPM = r.man.MinQualityPPM
	r.result.Valid = len(r.man.Segments) > 0
}

func readManifest(path string) (*manifest.Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m manifest.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func writeManifest(path string, m *manifest.Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return model.WrapError(model.StageManifest, "write", model.TrackRef{}, 0, "cannot encode manifest", err)
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return model.WrapError(model.StageManifest, "write", model.TrackRef{}, 0, "cannot write manifest", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return model.WrapError(model.StageManifest, "write", model.TrackRef{}, 0, "cannot publish manifest", err)
	}
	return nil
}
