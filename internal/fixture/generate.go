// Package fixture generates success, boundary and corrupted MP4 inputs locally.
package fixture

import (
	"encoding/json"
	"os"

	"mediaflow/internal/probe"
	"mediaflow/internal/safepath"
)

// Options controls demo fixture generation.
type Options struct {
	BatchKey     string
	BudgetMbps   float64
	QualityMin   float64
	ChunkSamples int
}

// GenerateDemo creates all accepted and negative-path assets and their manifest.
func GenerateDemo(workDir string, opt Options) (*probe.Manifest, string, error) {
	if opt.BatchKey == "" {
		opt.BatchKey = "demo-batch"
	}
	if opt.BudgetMbps == 0 {
		opt.BudgetMbps = 18
	}
	if opt.QualityMin == 0 {
		opt.QualityMin = 0.92
	}
	if opt.ChunkSamples == 0 {
		opt.ChunkSamples = 64
	}

	type asset struct {
		file string
		spec Spec
		mut  string
		item probe.Item
	}

	good := Spec{ID: "good-1080p30", Width: 1920, Height: 1080, FPS: 30, VideoSamples: 192, KeyframeEvery: 30, IncludeAudio: true}
	nonzero := good
	nonzero.ID = "boundary-nonzero-start"
	nonzero.CompositionOffset = 15000 // 500 ms at 30000 timescale
	sparse := good
	sparse.ID = "boundary-sparse-keyframes"
	sparse.KeyframeEvery = 192
	big := Spec{ID: "policy-8k60", Width: 7680, Height: 4320, FPS: 60, VideoSamples: 120, KeyframeEvery: 60, IncludeAudio: true}
	editBad := Spec{ID: "corrupt-edit-oob", Width: 1920, Height: 1080, FPS: 30, VideoSamples: 60, KeyframeEvery: 30, IncludeAudio: true, OOBEdit: true}
	lengthBad := Spec{ID: "corrupt-box-length", Width: 1920, Height: 1080, FPS: 30, VideoSamples: 24, KeyframeEvery: 12}
	truncBad := lengthBad
	truncBad.ID = "corrupt-truncated-box"
	missingAudio := Spec{ID: "invalid-missing-audio", Width: 1920, Height: 1080, FPS: 30, VideoSamples: 48, KeyframeEvery: 24}

	videoReq := probe.TrackExpectation{ID: 1, Type: "video", Timescale: 30000}
	audioReq := probe.TrackExpectation{ID: 2, Type: "audio", Timescale: 48000}
	bigVideoReq := probe.TrackExpectation{ID: 1, Type: "video", Timescale: 60000}

	assets := []asset{
		{file: "good.mp4", spec: good, item: probe.Item{ID: good.ID, Width: 1920, Height: 1080, FPS: 30, RequiredTracks: []probe.TrackExpectation{videoReq, audioReq}}},
		{file: "nonzero.mp4", spec: nonzero, item: probe.Item{ID: nonzero.ID, Width: 1920, Height: 1080, FPS: 30, RequiredTracks: []probe.TrackExpectation{videoReq, audioReq}}},
		{file: "sparse.mp4", spec: sparse, item: probe.Item{ID: sparse.ID, Width: 1920, Height: 1080, FPS: 30, RequiredTracks: []probe.TrackExpectation{videoReq, audioReq}}},
		{file: "impossible-8k.mp4", spec: big, item: probe.Item{ID: big.ID, Width: 7680, Height: 4320, FPS: 60, RequiredTracks: []probe.TrackExpectation{bigVideoReq, audioReq}}},
		{file: "edit-oob.mp4", spec: editBad, item: probe.Item{ID: editBad.ID, Width: 1920, Height: 1080, FPS: 30, RequiredTracks: []probe.TrackExpectation{videoReq, audioReq}}},
		{file: "bad-length.mp4", spec: lengthBad, mut: "length", item: probe.Item{ID: lengthBad.ID, Width: 1920, Height: 1080, FPS: 30, RequiredTracks: []probe.TrackExpectation{videoReq}}},
		{file: "truncated.mp4", spec: truncBad, mut: "truncate", item: probe.Item{ID: truncBad.ID, Width: 1920, Height: 1080, FPS: 30, RequiredTracks: []probe.TrackExpectation{videoReq}}},
		{file: "missing-audio.mp4", spec: missingAudio, item: probe.Item{ID: missingAudio.ID, Width: 1920, Height: 1080, FPS: 30, RequiredTracks: []probe.TrackExpectation{videoReq, audioReq}}},
	}

	items := make([]probe.Item, 0, len(assets))
	for _, a := range assets {
		full, err := safepath.Resolve(workDir, "media", a.file)
		if err != nil {
			return nil, "", err
		}
		if err := WriteAsset(full, a.spec); err != nil {
			return nil, "", err
		}
		switch a.mut {
		case "length":
			if err := CorruptBoxLength(full); err != nil {
				return nil, "", err
			}
		case "truncate":
			if err := TruncateBox(full); err != nil {
				return nil, "", err
			}
		}
		a.item.Path = "media/" + a.file
		items = append(items, a.item)
	}

	m := &probe.Manifest{
		BatchKey: opt.BatchKey, BudgetMbps: opt.BudgetMbps,
		QualityMin: opt.QualityMin, ChunkSamples: opt.ChunkSamples, Items: items,
	}
	manifestPath, err := safepath.Resolve(workDir, "manifest.json")
	if err != nil {
		return nil, "", err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(manifestPath, b, 0o644); err != nil {
		return nil, "", err
	}
	return m, manifestPath, nil
}
