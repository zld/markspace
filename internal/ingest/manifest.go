package ingest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediasync/internal/media"
)

const ManifestName = "manifest.json"

type TrackKind string

const (
	KindOpus     TrackKind = "opus"
	KindAAC      TrackKind = "aac"
	KindSubtitle TrackKind = "subtitle"
)

// Manifest is the small track inventory generated beside the media fixtures.
type Manifest struct {
	Schema       string      `json:"schema"`
	Seed         int64       `json:"seed"`
	Tracks       []TrackSpec `json:"tracks"`
	FrameTimesMS []int64     `json:"frame_times_ms"`
}

type TrackSpec struct {
	ID             string    `json:"id"`
	Kind           TrackKind `json:"kind"`
	Path           string    `json:"path"`
	TimeScale      int64     `json:"timescale"`
	OriginTicks    int64     `json:"origin_ticks"`
	ReorderWindow  int       `json:"reorder_window,omitempty"`
	ExpectedLoss   float64   `json:"expected_loss,omitempty"`
	MaxDeviationMS int64     `json:"max_deviation_ms,omitempty"`
}

type SubtitleFile struct {
	Cues []SubtitleCue `json:"cues"`
}

type SubtitleCue struct {
	Index            int    `json:"index"`
	StartMS          int64  `json:"start_ms"`
	EndMS            int64  `json:"end_ms"`
	ReferenceStartMS *int64 `json:"reference_start_ms,omitempty"`
	Text             string `json:"text"`
}

func LoadManifest(dir string) (*Manifest, string, error) {
	path := filepath.Join(dir, ManifestName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, path, media.InputError("ingest.manifest", "", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, path, media.InputError("ingest.manifest", "", fmt.Errorf("parse %s: %w", path, err)).
			WithOffset(0)
	}
	if err := m.Validate(); err != nil {
		return nil, path, err
	}
	return &m, path, nil
}

func (m *Manifest) Validate() error {
	if len(m.Tracks) == 0 {
		return media.InputError("ingest.manifest", "", fmt.Errorf("no tracks"))
	}
	seen := make(map[string]struct{}, len(m.Tracks))
	for i, t := range m.Tracks {
		if strings.TrimSpace(t.ID) == "" {
			return media.InputError("ingest.manifest", "", fmt.Errorf("tracks[%d]: empty id", i))
		}
		if _, ok := seen[t.ID]; ok {
			return media.InputError("ingest.manifest", t.ID, fmt.Errorf("duplicate track id"))
		}
		seen[t.ID] = struct{}{}
		switch t.Kind {
		case KindOpus, KindAAC, KindSubtitle:
		default:
			return media.InputError("ingest.manifest", t.ID, fmt.Errorf("unsupported kind %q", t.Kind))
		}
		if strings.TrimSpace(t.Path) == "" {
			return media.InputError("ingest.manifest", t.ID, fmt.Errorf("empty path"))
		}
		if t.ReorderWindow < 0 {
			return media.InputError("ingest.manifest", t.ID, fmt.Errorf("negative reorder window"))
		}
	}
	return nil
}

// TrackPath resolves a manifest-relative path without allowing escape from
// the input directory.
func TrackPath(dir, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", media.InputError("ingest.manifest", "", fmt.Errorf("absolute track path %q", rel))
	}
	base, err := filepath.Abs(dir)
	if err != nil {
		return "", media.InputError("ingest.manifest", "", err)
	}
	full := filepath.Join(base, filepath.FromSlash(rel))
	relBack, err := filepath.Rel(base, full)
	if err != nil || relBack == ".." || strings.HasPrefix(relBack, ".."+string(filepath.Separator)) {
		return "", media.InputError("ingest.manifest", "", fmt.Errorf("track path escapes input: %q", rel))
	}
	return full, nil
}

func LoadSubtitles(path, trackID string) (*SubtitleFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, media.InputError("ingest.subtitle", trackID, err)
	}
	var sf SubtitleFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, media.InputError("ingest.subtitle", trackID, fmt.Errorf("parse: %w", err)).WithOffset(0)
	}
	for i := range sf.Cues {
		c := &sf.Cues[i]
		if c.EndMS < c.StartMS {
			return nil, media.InputError("ingest.subtitle", trackID,
				fmt.Errorf("cue %d ends before start", c.Index)).WithRecord(c.Index)
		}
	}
	return &sf, nil
}
