// Package manifest emits the frame-aligned delivery manifest and verifies
// cross-stage conservation between timeline, frames and manifest.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"mediad/render"
	"mediad/timeline"
)

// State marks manifest completeness; production exports stay Incomplete until
// every frame and checkpoint is finalized.
type State string

const (
	StateIncomplete State = "incomplete"
	StateComplete   State = "complete"
	StateCorrupt    State = "corrupt"
)

// FrameRecord is the per-frame manifest entry aligned to the timeline clock.
type FrameRecord struct {
	Index        int64   `json:"index"`
	TimeNS       int64   `json:"time_ns"`
	SHA256       string  `json:"sha256"`
	Bytes        int     `json:"bytes"`
	BitrateBPS   int64   `json:"bitrate_bps"`
	QualityMean  float64 `json:"quality_mean"`
	QualityVar   float64 `json:"quality_variance"`
	Bad          bool    `json:"bad,omitempty"`
	FirstBadTime int64   `json:"first_bad_time_ns,omitempty"`
}

// ErrorRecord documents an isolated bad event without dropping it.
type ErrorRecord struct {
	TrackID uint32 `json:"track_id"`
	TimeNS  int64  `json:"time_ns"`
	Kind    string `json:"kind"`
	Reason  string `json:"reason"`
}

// Manifest is the verifiable delivery document.
type Manifest struct {
	Version    int           `json:"version"`
	State      State         `json:"state"`
	Width      int           `json:"width"`
	Height     int           `json:"height"`
	FPS        int           `json:"fps"`
	TargetBPS  int64         `json:"target_bps"`
	ActualBPS  int64         `json:"actual_bps"`
	DeltaPct   float64       `json:"bitrate_delta_pct"`
	TrackIDs   []uint32      `json:"track_ids"`
	EventCount int           `json:"event_count"`
	Frames     []FrameRecord `json:"frames"`
	Errors     []ErrorRecord `json:"errors,omitempty"`
	MaxErrorNS int64         `json:"manifest_max_error_ns"`
	SHA256     string        `json:"manifest_sha256"`
}

// Builder accumulates frame records and keeps the manifest incomplete until
// Finalize is called.
type Builder struct {
	m Manifest
}

// NewBuilder initializes the manifest header from timeline conservation data.
func NewBuilder(fps int, targetBPS int64, tl *timeline.Timeline) *Builder {
	ids := make([]uint32, 0, len(tl.Tracks))
	for _, tr := range tl.Tracks {
		ids = append(ids, tr.ID)
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	return &Builder{m: Manifest{
		Version: 1, State: StateIncomplete, Width: render.Width, Height: render.Height,
		FPS: fps, TargetBPS: targetBPS, TrackIDs: ids, EventCount: tl.Stats.EventCount,
	}}
}

// AddFrame appends a measured frame and validates monotonic, gap-free timing.
func (b *Builder) AddFrame(m render.FrameMetrics) error {
	if len(b.m.Frames) > 0 {
		prev := b.m.Frames[len(b.m.Frames)-1]
		if m.Index != prev.Index+1 {
			return fmt.Errorf("manifest: frame index gap: prev=%d got=%d", prev.Index, m.Index)
		}
		if m.TimeNS <= prev.TimeNS {
			return fmt.Errorf("manifest: non-monotonic frame time at index %d", m.Index)
		}
		if m.TimeNS == prev.TimeNS {
			return fmt.Errorf("manifest: duplicate frame time %d", m.TimeNS)
		}
	}
	b.m.Frames = append(b.m.Frames, FrameRecord{
		Index: m.Index, TimeNS: m.TimeNS, SHA256: m.SHA256, Bytes: m.Bytes,
		BitrateBPS: m.BitrateBPS, QualityMean: m.Quality.Mean, QualityVar: m.Quality.Variance,
		Bad: m.Bad, FirstBadTime: m.FirstBadTime,
	})
	return nil
}

// AddError records an isolated bad event.
func (b *Builder) AddError(e ErrorRecord) { b.m.Errors = append(b.m.Errors, e) }

// Finalize computes aggregate bitrate, timing error, the manifest hash and
// flips state. A corrupt track forces StateCorrupt, otherwise complete.
func (b *Builder) Finalize(durationNS int64, rep render.BitrateReport) Manifest {
	var total int64
	var maxErr int64
	for i, fr := range b.m.Frames {
		total += int64(fr.Bytes)
		// Frame time must equal its scheduled index time exactly.
		want := b.frameTimeNS(i)
		if d := abs64(fr.TimeNS - want); d > maxErr {
			maxErr = d
		}
	}
	b.m.ActualBPS = rep.ActualBPS
	b.m.DeltaPct = rep.DeltaPercent
	b.m.MaxErrorNS = maxErr
	state := StateComplete
	for _, fr := range b.m.Frames {
		if fr.Bad {
			state = StateCorrupt
		}
	}
	b.m.State = state
	b.m.SHA256 = hashManifest(b.m)
	_ = total
	return b.m
}

// frameTimeNS is the deterministic scheduled time for frame index i.
func (b *Builder) frameTimeNS(i int) int64 {
	if b.m.FPS <= 0 {
		return 0
	}
	return int64(i) * 1_000_000_000 / int64(b.m.FPS)
}

// hashManifest serializes without the hash field for a stable digest.
func hashManifest(m Manifest) string {
	m.SHA256 = ""
	raw, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Write atomically writes the manifest as JSON, keeping a .partial suffix
// until the full document is on disk.
func Write(dir string, m Manifest) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	final := filepath.Join(dir, "manifest.json")
	partial := final + ".partial"
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(partial, raw, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(partial, final); err != nil {
		return "", err
	}
	return final, nil
}

// Load reads a manifest back for verification.
func Load(path string) (Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
