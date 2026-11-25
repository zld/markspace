// Package fixture generates small deterministic MP4 and JSON fixtures. No
// fixture bytes are read from outside the repository or output directory.
package fixture

import (
	"encoding/json"
	"os"
	"path/filepath"

	"archivecheck/internal/model"
)

const (
	Timescale       uint32 = 90_000
	FrameDelta      uint32 = 3_000
	SampleCount     uint32 = 12
	StartTimestamp  int64  = 18_000
	EditStartTick   int64  = 18_000
	EditEndTick     int64  = 54_000
	TargetBoundary  int64  = 36_000
	RecoveredAnchor uint32 = 5 // 1-based sample at PTS 30,000
)

// GeneratedPaths lists files created by Generate.
type GeneratedPaths struct {
	Directory    string
	ValidInput   string
	Truncated    string
	BadEdit      string
	CorruptBox   string
	NoKeyframe   string
	AllManifests []string
}

// Generate writes success and failure fixtures into dir.
func Generate(dir string) (GeneratedPaths, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return GeneratedPaths{}, err
	}
	p := GeneratedPaths{Directory: dir}

	files := map[string][]byte{
		"valid.mp4":       BuildValidMP4(ValidSpec()),
		"no-keyframe.mp4": BuildNoKeyframeMP4(),
		"bad-empty.mp4":   BuildBadEditMP4("empty"),
		"bad-oob.mp4":     BuildBadEditMP4("oob"),
		"bad-reverse.mp4": BuildBadEditMP4("reverse"),
		"corrupt-box.mp4": BuildCorruptMP4(),
	}
	validBytes := BuildValidMP4(ValidSpec())
	truncated := append([]byte(nil), validBytes[:len(validBytes)-16]...)
	files["truncated.mp4"] = truncated

	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			return p, err
		}
	}

	manifests := map[string]model.InputManifest{
		"valid.json":            ValidManifest(),
		"truncated.json":        SingleManifest("truncated", "truncated.mp4", []model.EditInterval{ValidInterval()}),
		"corrupt-box.json":      SingleManifest("corrupt-box", "corrupt-box.mp4", []model.EditInterval{ValidInterval()}),
		"no-keyframe.json":      SingleManifest("no-keyframe", "no-keyframe.mp4", []model.EditInterval{ValidInterval()}),
		"bad-edit-empty.json":   BadEditManifest("bad-empty", "bad-empty.mp4", 0),
		"bad-edit-oob.json":     BadEditManifest("bad-oob", "bad-oob.mp4", 1),
		"bad-edit-reverse.json": BadEditManifest("bad-reverse", "bad-reverse.mp4", 2),
		"bad-edit.json":         AggregateBadEditManifest(),
	}
	for name, m := range manifests {
		b, err := model.CanonicalJSON(m)
		if err != nil {
			return p, err
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			return p, err
		}
		p.AllManifests = append(p.AllManifests, filepath.Join(dir, name))
	}
	p.ValidInput = filepath.Join(dir, "valid.json")
	p.Truncated = filepath.Join(dir, "truncated.json")
	p.BadEdit = filepath.Join(dir, "bad-edit.json")
	p.CorruptBox = filepath.Join(dir, "corrupt-box.json")
	p.NoKeyframe = filepath.Join(dir, "no-keyframe.json")

	index, err := model.CanonicalJSON(map[string]string{
		"valid": p.ValidInput, "truncated": p.Truncated, "bad_edit": p.BadEdit,
	})
	if err != nil {
		return p, err
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), index, 0o644); err != nil {
		return p, err
	}
	return p, nil
}

func ValidInterval() model.EditInterval {
	return model.EditInterval{Index: 0, StartTick: EditStartTick, EndTick: EditEndTick}
}

func ValidManifest() model.InputManifest {
	return model.InputManifest{
		SchemaVersion: model.ManifestSchema,
		Description:   "deterministic sparse-keyframe archive fixture",
		Bitrates:      []int64{400_000, 900_000},
		Assets: []model.InputAsset{{
			ID: "asset-valid", Path: "valid.mp4", TrackID: 1,
			StartTimestampTicks: StartTimestamp,
			EditIntervals:       []model.EditInterval{ValidInterval()},
		}},
	}
}

func SingleManifest(id, path string, intervals []model.EditInterval) model.InputManifest {
	return model.InputManifest{
		SchemaVersion: model.ManifestSchema,
		Bitrates:      []int64{400_000, 900_000},
		Assets: []model.InputAsset{{
			ID: id, Path: path, TrackID: 1,
			StartTimestampTicks: StartTimestamp, EditIntervals: intervals,
		}},
	}
}

func BadEditManifest(id, path string, index int) model.InputManifest {
	in := ValidInterval()
	in.Index = index
	switch index {
	case 0:
		in.Empty = true
	case 1:
		in.StartTick = 999_999
		in.EndTick = 1_099_999
	case 2:
		in.StartTick, in.EndTick = in.EndTick, in.StartTick
	}
	return SingleManifest(id, path, []model.EditInterval{in})
}

func AggregateBadEditManifest() model.InputManifest {
	m := model.InputManifest{SchemaVersion: model.ManifestSchema, Bitrates: []int64{400_000, 900_000}}
	for _, x := range []struct {
		id, file string
		idx      int
	}{
		{"bad-empty", "bad-empty.mp4", 0},
		{"bad-oob", "bad-oob.mp4", 1},
		{"bad-reverse", "bad-reverse.mp4", 2},
	} {
		bm := BadEditManifest(x.id, x.file, x.idx)
		m.Assets = append(m.Assets, bm.Assets[0])
	}
	return m
}

// LoadManifest reads a JSON manifest.
func LoadManifest(path string) (model.InputManifest, error) {
	var m model.InputManifest
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	return m, nil
}
