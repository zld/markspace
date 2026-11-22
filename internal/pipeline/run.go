// Package pipeline wires the timeline, transport-recovery and media-analysis
// subsystems into one deterministic recovery flow. All cross-subsystem state —
// the unified axis, recovery gaps and per-packet timeline coordinates — is
// created here and passed explicitly, so no subsystem keeps a private notion
// of media time or error handling.
package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/archive/mediakernel/internal/manifest"
	"github.com/archive/mediakernel/internal/media"
	"github.com/archive/mediakernel/internal/rtprecover"
	"github.com/archive/mediakernel/internal/timeline"
)

// Config captures every tunable for one run. Values come from CLI flags; zero
// fields are replaced with safe defaults by Normalize.
type Config struct {
	WorkDir         string
	ManifestPath    string
	ReorderWindow   int
	LossThreshold   int
	TargetBitrate     int64
	SegmentDurationUS int64
	Silence         media.SilenceConfig
	Fingerprint     media.FingerprintConfig
	// CorruptIndex forces an on-disk index digest mismatch to exercise rebuild.
	CorruptIndex bool
	// Resume continues from the last checkpoint instead of starting over.
	Resume bool
	// MaxScanSeconds bounds how much material a run may process.
	MaxScanSeconds int
}

// IndexRecord is the persisted index envelope. A digest mismatch invalidates
// the index unconditionally and forces a rescan — there is no "trust anyway".
type IndexRecord struct {
	InputDigest string          `json:"input_digest"`
	Index       *media.Index    `json:"index"`
}

// indexPath is the fixed on-disk location next to the work directory.
func indexPath(workDir string) string {
	return filepath.Join(workDir, "index.json")
}

// checkpointPath is where resume state lives.
func checkpointPath(workDir string) string {
	return filepath.Join(workDir, "checkpoint.json")
}

// loadIndex reads a persisted index and validates it against the current input
// digest. A missing file is normal; a mismatched or unreadable index returns
// rebuilt=true so the caller rescans rather than trusting stale state.
func loadIndex(workDir, digest string) (rec *IndexRecord, rebuilt bool, err error) {
	raw, readErr := os.ReadFile(indexPath(workDir))
	if os.IsNotExist(readErr) {
		return nil, true, nil
	}
	if readErr != nil {
		return nil, true, timeline.WrapError(timeline.KindCorrupt, "index read failed", readErr)
	}
	var stored IndexRecord
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, true, nil // malformed index: invalidate and rebuild
	}
	if stored.InputDigest != digest || stored.Index == nil || stored.Index.Digest != digest {
		return nil, true, nil // digest change invalidates the index
	}
	return &stored, false, nil
}

// saveIndex persists a freshly built index atomically-ish (tmp + rename).
func saveIndex(workDir string, rec *IndexRecord) error {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return timeline.WrapError(timeline.KindInternal, "workdir create failed", err)
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return timeline.WrapError(timeline.KindInternal, "index encode failed", err)
	}
	path := indexPath(workDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return timeline.WrapError(timeline.KindInternal, "index write failed", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return timeline.WrapError(timeline.KindInternal, "index rename failed", err)
	}
	return nil
}

// loadCheckpoint returns the persisted checkpoint, or nil when none exists.
func loadCheckpoint(workDir string) (*manifest.Checkpoint, error) {
	raw, err := os.ReadFile(checkpointPath(workDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, timeline.WrapError(timeline.KindCorrupt, "checkpoint read failed", err)
	}
	var cp manifest.Checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, timeline.WrapError(timeline.KindCorrupt, "checkpoint malformed", err)
	}
	return &cp, nil
}

func saveCheckpoint(workDir string, cp *manifest.Checkpoint) error {
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return timeline.WrapError(timeline.KindInternal, "checkpoint encode failed", err)
	}
	tmp := checkpointPath(workDir) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return timeline.WrapError(timeline.KindInternal, "checkpoint write failed", err)
	}
	return os.Rename(tmp, checkpointPath(workDir))
}

// WritePartialCheckpoint simulates a run that crashed after completing the
// given number of segments at lastTimelineUS. The subsequent Run with
// Config.Resume treats that boundary as recovered instead of redoing the work.
func WritePartialCheckpoint(workDir string, completedSegments int, lastTimelineUS int64) error {
	return saveCheckpoint(workDir, &manifest.Checkpoint{
		CompletedSegments: completedSegments,
		LastTimelineUS:    lastTimelineUS,
	})
}

// ClearCheckpoint removes resume state, used to reset a work directory.
func ClearCheckpoint(workDir string) error {
	if err := os.Remove(checkpointPath(workDir)); err != nil && !os.IsNotExist(err) {
		return timeline.WrapError(timeline.KindInternal, "checkpoint remove failed", err)
	}
	return nil
}

// Normalize fills defaults and validates the configuration.
func (c *Config) Normalize() error {
	if c.WorkDir == "" {
		return timeline.NewError(timeline.KindInvalidInput, "work directory required")
	}
	if c.ReorderWindow <= 0 {
		c.ReorderWindow = 32
	}
	if c.ReorderWindow > rtprecover.MaxWindow {
		return timeline.NewError(timeline.KindLimit, "reorder window %d exceeds limit %d", c.ReorderWindow, rtprecover.MaxWindow)
	}
	if c.LossThreshold <= 0 {
		c.LossThreshold = 8
	}
	if c.TargetBitrate <= 0 {
		c.TargetBitrate = 2_400_000
	}
	if c.SegmentDurationUS <= 0 {
		c.SegmentDurationUS = int64(10 * timeline.Second)
	}
	if c.MaxScanSeconds <= 0 {
		c.MaxScanSeconds = 24 * 3600
	}
	if c.Silence.Window == 0 {
		c.Silence = media.DefaultSilenceConfig()
	}
	return c.Fingerprint.Validate()
}
