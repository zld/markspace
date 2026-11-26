// Package fixture generates small deterministic media and manifest samples.
package fixture

import (
	"encoding/json"
	"os"
	"path/filepath"

	"medpipe/internal/manifest"
)

// Options controls fixture generation.
type Options struct {
	Dir  string
	Seed int64
}

// Result lists generated files.
type Result struct {
	Dir       string   `json:"dir"`
	Seed      int64    `json:"seed"`
	Manifests []string `json:"manifests"`
	Media     []string `json:"media"`
}

// Generate creates normal, corrupt-range, bad-boundary and interrupt samples.
func Generate(opts Options) (*Result, error) {
	if opts.Dir == "" {
		opts.Dir = ".tmp/fixtures"
	}
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, err
	}
	seed := uint32(opts.Seed)
	res := &Result{Dir: opts.Dir, Seed: opts.Seed}

	normalMedia, err := writeNormalMedia(opts.Dir, seed)
	if err != nil {
		return nil, err
	}
	res.Media = append(res.Media, normalMedia...)
	badMedia, err := writeBadMedia(opts.Dir, seed)
	if err != nil {
		return nil, err
	}
	res.Media = append(res.Media, badMedia...)

	normal := &manifest.Manifest{
		SchemaVersion: 1,
		Parameters:    manifest.Parameters{WrapBits: 32, BatchSamples: 4},
		Tracks: []manifest.Track{
			{ID: "wrap", Path: "wrap.mp4", Kind: "video"},
			{ID: "edit", Path: "edit.mp4", Kind: "audio"},
			{ID: "jump", Path: "jump.mp4", Kind: "video"},
		},
		Switches: []manifest.Switch{{
			ID: "switch-jump", Track: "jump", Sample: 4,
			LeftSource: "camera-A", RightSource: "camera-B",
		}},
		Dependencies: []manifest.Dependency{{
			Stage: "conform", Requires: []string{"wrap", "edit", "jump"},
		}},
	}
	badRange := &manifest.Manifest{
		SchemaVersion: 1,
		Tracks:        []manifest.Track{{ID: "range-bad", Path: "bad-range.mp4", Kind: "video"}},
	}
	badJump := &manifest.Manifest{
		SchemaVersion: 1,
		Tracks:        []manifest.Track{{ID: "bad-jump", Path: "bad-jump.mp4", Kind: "video"}},
		Switches: []manifest.Switch{{
			ID: "switch-bad-jump", Track: "bad-jump", Sample: 4,
			LeftSource: "camera-A", RightSource: "camera-B",
		}},
		Dependencies: []manifest.Dependency{
			{Stage: "align-bad-jump", Requires: []string{"bad-jump"}},
			{Stage: "package-bad-jump", Requires: []string{"align-bad-jump"}},
		},
	}

	files := map[string]*manifest.Manifest{
		"normal.json":    normal,
		"bad-range.json": badRange,
		"bad-jump.json":  badJump,
	}
	for name, m := range files {
		path, err := writeManifest(opts.Dir, name, m)
		if err != nil {
			return nil, err
		}
		res.Manifests = append(res.Manifests, path)
	}
	// The interrupt case uses normal.json plus the run's --fault flag.  Keep a
	// named copy so operators can discover the intended recovery invocation.
	if err := copyFile(filepath.Join(opts.Dir, "normal.json"), filepath.Join(opts.Dir, "interrupt.json")); err != nil {
		return nil, err
	}
	res.Manifests = append(res.Manifests, filepath.Join(opts.Dir, "interrupt.json"))
	return res, nil
}

func writeNormalMedia(dir string, seed uint32) ([]string, error) {
	specs := []TrackSpec{
		{
			FileName: "wrap.mp4", HandlerType: "vide", Timescale: 90000, SampleDuration: 3000, Seed: seed,
			RTPTimestamps: []uint32{
				^uint32(0) - 9000, ^uint32(0) - 6000, ^uint32(0) - 3000, ^uint32(0), 2999, 5999,
			},
			SyncSamples: []uint32{1, 4},
		},
		{
			FileName: "edit.mp4", HandlerType: "soun", Timescale: 48000, SampleDuration: 1024, Seed: seed + 1,
			RTPTimestamps:       []uint32{0, 1024, 2048, 3072, 4096, 5120},
			SyncSamples:         []uint32{1, 3, 5},
			EmptyEditMovieTicks: 100,
			MediaEditMovieTicks: 128,
		},
		{
			FileName: "jump.mp4", HandlerType: "vide", Timescale: 90000, SampleDuration: 3000, Seed: seed + 2,
			RTPTimestamps: []uint32{0, 3000, 6000, 7000, 10000, 13000},
			SyncSamples:   []uint32{1, 4},
		},
	}
	paths := make([]string, 0, len(specs))
	for _, sp := range specs {
		p, err := WriteTrackMP4(dir, sp)
		if err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, nil
}

func writeBadMedia(dir string, seed uint32) ([]string, error) {
	rangePath, err := WriteTrackMP4(dir, TrackSpec{
		FileName: "bad-range.mp4", HandlerType: "vide", Timescale: 90000, SampleDuration: 3000, Seed: seed + 3,
		RTPTimestamps:  []uint32{100, 3100, 6100},
		SyncSamples:    []uint32{1},
		OversizeSample: 2,
		OversizeBytes:  999999,
	})
	if err != nil {
		return nil, err
	}
	jumpPath, err := WriteTrackMP4(dir, TrackSpec{
		FileName: "bad-jump.mp4", HandlerType: "vide", Timescale: 90000, SampleDuration: 3000, Seed: seed + 4,
		RTPTimestamps: []uint32{0, 3000, 6000, 540000, 543000, 546000},
		SyncSamples:   []uint32{1, 4},
	})
	if err != nil {
		return nil, err
	}
	return []string{rangePath, jumpPath}, nil
}

func writeManifest(dir, name string, m *manifest.Manifest) (string, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
