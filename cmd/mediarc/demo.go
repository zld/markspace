package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/zld/mediarc/internal/container"
	"github.com/zld/mediarc/internal/fixture"
	"github.com/zld/mediarc/internal/manifest"
	"github.com/zld/mediarc/internal/model"
	"github.com/zld/mediarc/internal/segment"
	"github.com/zld/mediarc/internal/timeline"
)

func runDemo(args []string) error {
	o, err := parseDemoFlags(args)
	if err != nil {
		return err
	}
	if o.lateEvent {
		return runLateEvent(o.checkpoint)
	}
	if o.corrupt == "truncated-box" {
		return runCorruptDemo(o)
	}
	return runHealthyDemo(o)
}

func runHealthyDemo(o demoOptions) error {
	asset, err := fixture.GenerateDemo(o.seed)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(o.out, 0o755); err != nil {
		return err
	}
	manifestPath := filepath.Join(o.out, "manifest.json")
	checkpointPath := filepath.Join(o.out, "checkpoint.json")
	_ = os.Remove(manifestPath)

	inputPath := filepath.Join(o.out, asset.InputName)
	if err := os.WriteFile(inputPath, asset.MP4, 0o644); err != nil {
		return err
	}
	f, rt, raw, metas, err := parseAsset(inputPath, asset.Config)
	if err != nil {
		return err
	}
	built, cut, m, err := buildManifest(o.seed, asset.InputName, asset.Config, f, rt, raw, metas, asset.Payload)
	if err != nil {
		return err
	}
	for i := range cut.Segments {
		for j := range cut.Segments[i].Representations {
			rel := fmt.Sprintf("chunks/segment-%03d-rep-%d.bin", cut.Segments[i].Index, cut.Segments[i].Representations[j].TrackID)
			full := filepath.Join(o.out, rel)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(full, cut.Segments[i].Representations[j].ChunkBytes, 0o644); err != nil {
				return err
			}
			cut.Segments[i].Representations[j].ChunkFile = rel
		}
	}
	// Recreate the manifest after assigning relative chunk file names.
	m = manifest.New(o.seed, manifest.InputInfo{
		File: asset.InputName, SHA256: f.SHA256, Size: f.Size,
	}, asset.Config, cut, rt.Rate)
	m.Issues = append(m.Issues, built.Issues...)
	if err := manifest.Write(manifestPath, m); err != nil {
		return err
	}
	matched, total, err := manifest.VerifyChunkFiles(o.out, m)
	if err != nil {
		return err
	}
	if matched != total || total != len(cut.Segments) {
		return &model.IntegrityError{Path: manifestPath, Err: fmt.Errorf("segment hash verification %d/%d", matched, total)}
	}

	cp := built.Checkpoint(timeline.InputSummary{
		Path: asset.InputName, SHA256: f.SHA256, Size: f.Size,
	}, asset.Config, m.ManifestSHA256)
	if err := timeline.SaveCheckpoint(checkpointPath, cp); err != nil {
		return err
	}

	fmt.Printf("roundtrip_rate=%.3f\n", rt.Rate)
	fmt.Printf("keyframe_hit_rate=%.3f\n", m.Metrics.KeyframeHitRate)
	fmt.Printf("segment_hash_match=%d/%d\n", matched, total)
	fmt.Printf("segments=%d\n", len(m.Segments))
	fmt.Printf("manifest_sha256=%s\n", m.ManifestSHA256)
	fmt.Printf("checkpoint=%s\n", checkpointPath)
	return nil
}

func runCorruptDemo(o demoOptions) error {
	asset, err := fixture.TruncatedBoxAsset(o.seed)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(o.out, 0o755); err != nil {
		return err
	}
	// A stale success document must never be mistaken for this failed run.
	_ = os.Remove(filepath.Join(o.out, "manifest.json"))
	_ = os.Remove(filepath.Join(o.out, "checkpoint.json"))

	inputPath := filepath.Join(o.out, asset.InputName)
	if err := os.WriteFile(inputPath, asset.MP4, 0o644); err != nil {
		return err
	}
	_, err = container.ReadPath(inputPath, asset.Config)
	if err == nil {
		return &model.IntegrityError{Path: inputPath, Err: fmt.Errorf("corrupt fixture was accepted")}
	}
	// Ensure no manifest exists even if an unexpected file was created elsewhere.
	_ = os.Remove(filepath.Join(o.out, "manifest.json"))
	return err
}

func parseAsset(inputPath string, cfg model.Config) (*container.File, *container.RoundtripReport, []model.RawEvent, []model.TrackMetadata, error) {
	f, err := container.ReadPath(inputPath, cfg)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	rt, err := container.VerifyRoundtrip(f)
	if err != nil {
		return nil, nil, nil, nil, &model.IntegrityError{Path: inputPath, Err: err}
	}
	var raw []model.RawEvent
	metas := make([]model.TrackMetadata, 0, len(f.Tracks))
	for _, tr := range f.Tracks {
		raw = append(raw, tr.Events...)
		metas = append(metas, tr.Metadata)
	}
	return f, rt, raw, metas, nil
}

func buildManifest(seed int64, inputName string, cfg model.Config, f *container.File, rt *container.RoundtripReport, raw []model.RawEvent, metas []model.TrackMetadata, payload segment.PayloadFunc) (*timeline.Built, *segment.Report, *manifest.Manifest, error) {
	built, err := timeline.Build(raw, cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	cut, err := segment.Cut(built, metas, cfg, payload)
	if err != nil {
		return nil, nil, nil, err
	}
	m := manifest.New(seed, manifest.InputInfo{File: inputName, SHA256: f.SHA256, Size: f.Size}, cfg, cut, rt.Rate)
	m.Issues = append(m.Issues, built.Issues...)
	return built, cut, m, nil
}
