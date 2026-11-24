package main

import (
	"fmt"
	"path/filepath"

	"github.com/zld/mediarc/internal/fixture"
	"github.com/zld/mediarc/internal/manifest"
	"github.com/zld/mediarc/internal/model"
	"github.com/zld/mediarc/internal/segment"
	"github.com/zld/mediarc/internal/timeline"
)

func runLateEvent(checkpointPath string) error {
	cp, err := timeline.LoadCheckpoint(checkpointPath)
	if err != nil {
		return err
	}
	inputPath := cp.ResolveInputPath(checkpointPath)
	data, err := cp.VerifyFile(inputPath)
	if err != nil {
		return err
	}

	f, rt, baseRaw, metas, err := parseAsset(inputPath, cp.Config)
	if err != nil {
		return err
	}
	if f.SHA256 != cp.Input.SHA256 {
		return &model.IntegrityError{Path: inputPath, Err: model.ErrDigestMismatch}
	}

	// The late sample is deterministic fixture data keyed by the checkpoint's
	// seed. Its payload is also served by asset.Payload during cutting.
	asset, err := fixture.GenerateDemo(cp.Config.Seed)
	if err != nil {
		return err
	}
	late := asset.LateEvent

	fullRaw := append(append([]model.RawEvent(nil), baseRaw...), late)
	fullBuilt, err := timeline.Build(fullRaw, cp.Config)
	if err != nil {
		return err
	}
	fullCut, err := segment.Cut(fullBuilt, metas, cp.Config, asset.Payload)
	if err != nil {
		return err
	}
	fullManifest := manifest.New(cp.Config.Seed, manifest.InputInfo{
		File: cp.Input.Path, SHA256: f.SHA256, Size: int64(len(data)),
	}, cp.Config, fullCut, rt.Rate)
	fullManifest.Issues = append(fullManifest.Issues, fullBuilt.Issues...)
	if err := fullManifest.Finalize(); err != nil {
		return err
	}

	rec, err := timeline.RecomputeFromCheckpoint(cp, baseRaw, late)
	if err != nil {
		return err
	}
	if !timeline.SameEventTimes(fullBuilt, rec.Result) {
		return &model.IntegrityError{Path: checkpointPath, Err: fmt.Errorf("checkpoint recomputation differs from full ordered processing")}
	}
	recCut, err := segment.Cut(rec.Result, metas, cp.Config, asset.Payload)
	if err != nil {
		return err
	}
	recManifest := manifest.New(cp.Config.Seed, manifest.InputInfo{
		File: cp.Input.Path, SHA256: f.SHA256, Size: int64(len(data)),
	}, cp.Config, recCut, rt.Rate)
	recManifest.Issues = append(recManifest.Issues, rec.Result.Issues...)
	if err := recManifest.Finalize(); err != nil {
		return err
	}
	if recManifest.ManifestSHA256 != fullManifest.ManifestSHA256 {
		return &model.IntegrityError{
			Path: checkpointPath,
			Err:  fmt.Errorf("recomputed manifest %s != full manifest %s", recManifest.ManifestSHA256, fullManifest.ManifestSHA256),
		}
	}

	wm := timeline.NewWatermark(cp.Config.AllowedLatenessNS)
	wm.Restore(cp.Watermark)
	var lateTime model.NS
	for _, ev := range rec.Result.Ordered {
		if ev.Raw.SampleNumber == late.SampleNumber {
			lateTime = ev.TimeNS
			break
		}
	}
	disposition := wm.Observe(lateTime)
	outManifest := filepath.Join(filepath.Dir(checkpointPath), "manifest-recomputed.json")
	if err := manifest.Write(outManifest, recManifest); err != nil {
		return err
	}

	fmt.Printf("recomputed=%t\n", rec.Recomputed)
	fmt.Printf("late_disposition=%s\n", disposition)
	fmt.Printf("manifest_sha256=%s\n", recManifest.ManifestSHA256)
	fmt.Printf("full_manifest_sha256=%s\n", fullManifest.ManifestSHA256)
	fmt.Printf("manifest_sha256_match=true\n")
	fmt.Printf("recomputed_manifest=%s\n", outManifest)
	return nil
}
