// Package manifest reads and writes the material lists that describe archive
// inputs and the recovery outputs produced from them.
package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/archive/mediakernel/internal/timeline"
)

// Limits applied while reading manifests. They make denial-of-service via a
// hostile manifest impossible and keep every later allocation bounded.
const (
	MaxManifestBytes = 8 << 20 // 8 MiB
	MaxAssets        = 1024
	MaxEditsPerAsset = 256
	MaxPathLength    = 4096
)

// EditEntry is one raw edit-list entry in the manifest. All times are in
// microseconds on the container's media-local clock.
type EditEntry struct {
	MediaStartUS int64 `json:"media_start_us"`
	StartUS      int64 `json:"timeline_start_us"`
	DurationUS   int64 `json:"duration_us"`
}

// Asset is one input material description.
type Asset struct {
	ID             string      `json:"id"`
	MP4Path        string      `json:"mp4_path"`
	RTPPath        string      `json:"rtp_path"`
	Timescale      uint32      `json:"timescale"`
	StartOffsetUS  int64       `json:"start_offset_us"` // non-zero media start
	ClockRate      uint32      `json:"rtp_clock_rate"`
	Edits          []EditEntry `json:"edits,omitempty"`
	TargetBitrate  int64       `json:"target_bitrate_bps"`
	ExpectedDigest string      `json:"expected_digest,omitempty"`
}

// StartOffset returns the non-zero start timestamp as an axis time.
func (a Asset) StartOffset() timeline.Time { return timeline.Time(a.StartOffsetUS) }

// Manifest is the full input list.
type Manifest struct {
	Version int      `json:"version"`
	Assets  []*Asset `json:"assets"`
}

// ReadFile loads, size-limits, decodes and validates a manifest. All paths are
// resolved against baseDir and confined to it: absolute paths, traversal
// sequences and symlink escapes are rejected.
func ReadFile(path, baseDir string) (*Manifest, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, timeline.WrapError(timeline.KindInvalidInput, "manifest stat failed", err)
	}
	if info.Size() > MaxManifestBytes {
		return nil, timeline.NewError(timeline.KindLimit,
			"manifest size %d exceeds limit %d", info.Size(), MaxManifestBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, timeline.WrapError(timeline.KindInvalidInput, "manifest read failed", err)
	}
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, timeline.WrapError(timeline.KindInvalidInput, "manifest JSON invalid", err)
	}
	if err := m.Validate(baseDir); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate enforces structural limits and path safety for every asset.
func (m *Manifest) Validate(baseDir string) error {
	if m.Version != 1 {
		return timeline.NewError(timeline.KindInvalidInput, "unsupported manifest version %d", m.Version)
	}
	if len(m.Assets) == 0 {
		return timeline.NewError(timeline.KindInvalidInput, "manifest contains no assets")
	}
	if len(m.Assets) > MaxAssets {
		return timeline.NewError(timeline.KindLimit, "asset count %d exceeds limit %d", len(m.Assets), MaxAssets)
	}
	seen := make(map[string]bool, len(m.Assets))
	for i, a := range m.Assets {
		if a == nil {
			return timeline.NewError(timeline.KindInvalidInput, "asset %d is null", i)
		}
		if a.ID == "" {
			return timeline.NewError(timeline.KindInvalidInput, "asset %d has empty id", i)
		}
		if seen[a.ID] {
			return timeline.NewError(timeline.KindInvalidInput, "duplicate asset id %q", a.ID)
		}
		seen[a.ID] = true
		if a.Timescale == 0 || a.Timescale > timeline.MaxTimescale {
			return timeline.NewError(timeline.KindInvalidInput,
				"asset %q timescale %d invalid", a.ID, a.Timescale)
		}
		if a.ClockRate == 0 || a.ClockRate > timeline.MaxTimescale {
			return timeline.NewError(timeline.KindInvalidInput,
				"asset %q rtp_clock_rate %d invalid", a.ID, a.ClockRate)
		}
		if a.TargetBitrate <= 0 {
			return timeline.NewError(timeline.KindInvalidInput,
				"asset %q target_bitrate_bps must be positive", a.ID)
		}
		if timeline.Time(a.StartOffsetUS) < 0 || timeline.Time(a.StartOffsetUS) > timeline.MaxTime {
			return timeline.NewError(timeline.KindInvalidInput,
				"asset %q start offset out of range", a.ID)
		}
		if len(a.Edits) > MaxEditsPerAsset {
			return timeline.NewError(timeline.KindLimit,
				"asset %q edit count %d exceeds limit %d", a.ID, len(a.Edits), MaxEditsPerAsset)
		}
		for j, e := range a.Edits {
			if e.DurationUS <= 0 {
				return timeline.NewError(timeline.KindInvalidInput,
					"asset %q edit %d has non-positive duration", a.ID, j)
			}
			if e.MediaStartUS < a.StartOffsetUS {
				return timeline.NewError(timeline.KindInvalidInput,
					"asset %q edit %d starts before media start offset", a.ID, j)
			}
		}
		if err := safePath(a.MP4Path, baseDir); err != nil {
			return timeline.WrapError(timeline.KindInvalidInput,
				fmt.Sprintf("asset %q mp4_path unsafe", a.ID), err)
		}
		if err := safePath(a.RTPPath, baseDir); err != nil {
			return timeline.WrapError(timeline.KindInvalidInput,
				fmt.Sprintf("asset %q rtp_path unsafe", a.ID), err)
		}
	}
	return nil
}

// SafePath resolves p against baseDir, rejecting absolute inputs, traversal and
// symlink escapes. The returned path is the cleaned absolute path.
func SafePath(p, baseDir string) (string, error) {
	if p == "" {
		return "", timeline.NewError(timeline.KindInvalidInput, "empty path")
	}
	if len(p) > MaxPathLength {
		return "", timeline.NewError(timeline.KindLimit, "path length %d exceeds limit %d", len(p), MaxPathLength)
	}
	if filepath.IsAbs(p) {
		return "", timeline.NewError(timeline.KindInvalidInput, "absolute paths are not allowed: %s", p)
	}
	base, err := filepath.Abs(filepath.Clean(baseDir))
	if err != nil {
		return "", timeline.WrapError(timeline.KindInvalidInput, "base dir resolution failed", err)
	}
	joined := filepath.Join(base, filepath.Clean(p))
	// Reject any traversal that escapes the base after cleaning.
	rel, err := filepath.Rel(base, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", timeline.NewError(timeline.KindInvalidInput, "path escapes base directory: %s", p)
	}
	// Reject symlink chains pointing outside the base. The final component may
	// not exist yet for output paths; check every existing ancestor.
	for dir := joined; dir != base && dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		real, err := filepath.EvalSymlinks(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", timeline.WrapError(timeline.KindInvalidInput, "symlink evaluation failed", err)
		}
		if !isWithin(real, base) {
			return "", timeline.NewError(timeline.KindInvalidInput, "symlink escapes base directory: %s", p)
		}
	}
	return joined, nil
}

func safePath(p, baseDir string) error {
	_, err := SafePath(p, baseDir)
	return err
}

func isWithin(p, base string) bool {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
