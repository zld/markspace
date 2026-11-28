// Package pipeline is the batch-processing kernel. It scans inputs into the
// time line and index, aligns renditions, and produces a reviewable manifest.
// The command in cmd/mediad only parses flags and prints results; everything it
// does is a call into this package.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"mediad/index"
	"mediad/media"
	"mediad/timeline"
)

// Config controls one batch run. It is hashed into the checkpoint so a resumed
// run cannot silently change the partitioning or the acceptance thresholds.
type Config struct {
	// ChunkSize is the number of segments per checkpointed work chunk.
	ChunkSize int
	// SegmentTargetUs is the desired segment duration.
	SegmentTargetUs int64
	// MaxBoundaryErrorUs is the per-rendition keyframe tolerance.
	MaxBoundaryErrorUs int64
	// ReorderWindow is the RTP sequence reordering window in packets.
	ReorderWindow int64
	// LossBudget is the fraction of recoverable loss the run warns above.
	LossBudget float64
	// AVSyncBudgetMs is the subtitle/audio sync tolerance for reporting.
	AVSyncBudgetMs float64
}

// DefaultConfig returns the acceptance thresholds.
func DefaultConfig() Config {
	return Config{
		ChunkSize:          64,
		SegmentTargetUs:    2_000_000,
		MaxBoundaryErrorUs: 34_000, // ~1 frame at 30 fps
		ReorderWindow:      media.DefaultReorderWindow,
		LossBudget:         0.05,
		AVSyncBudgetMs:     40,
	}
}

// Sum is a stable digest of every field that changes output.
func (c Config) Sum() string {
	parts := []string{
		fmt.Sprintf("chunk=%d", c.ChunkSize),
		fmt.Sprintf("target_us=%d", c.SegmentTargetUs),
		fmt.Sprintf("boundary_us=%d", c.MaxBoundaryErrorUs),
		fmt.Sprintf("reorder=%d", c.ReorderWindow),
		fmt.Sprintf("loss=%.6f", c.LossBudget),
		fmt.Sprintf("avsync_ms=%.6f", c.AVSyncBudgetMs),
	}
	return index.ConfigSum(parts...)
}

// Input is one media file to process.
type Input struct {
	Path  string
	Bytes []byte
	// IsRTP marks a packet-capture fixture instead of an ISO-BMFF file.
	IsRTP bool
}

// ScanResult is what scanning one input produced.
type ScanResult struct {
	Input       Input
	Fingerprint index.Fingerprint
	Clip        *media.Clip
	Events      []timeline.Event
	Issues      timeline.Issues
	// IndexReused is true when a stored parse index matched the container.
	IndexReused bool
	// RTP holds loss-recovery results for a packet-capture input; nil for an
	// ISO-BMFF input.
	RTP *media.RTPRecovery
	// IsRTP records whether the input was a packet capture, so re-fingerprinting
	// at commit time uses the content-only digest.
	IsRTP bool
}

// displayPath renders an input path relative to the job's work directory so a
// manifest produced under one directory can be compared byte-for-byte with one
// produced under another. A path outside the work directory is returned
// unchanged.
func (j *Job) displayPath(path string) string {
	if j.dir == "" {
		return path
	}
	rel, err := filepath.Rel(j.dir, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

// sha256Hex is the canonical hex digest used across the pipeline.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// sortedKeys returns map keys in order for deterministic iteration.
func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// joinIssues merges issue lists into a sorted, de-duplicated list.
func joinIssues(lists ...timeline.Issues) timeline.Issues {
	var out timeline.Issues
	for _, l := range lists {
		out.Merge(l)
	}
	return out.Sorted()
}

// issueSummary counts issues by code for the report.
func issueSummary(is timeline.Issues) map[string]int {
	out := map[string]int{}
	for _, i := range is {
		out[string(i.Code)]++
	}
	return out
}

// formatIssues renders issues one per line with a count prefix, for the CLI.
func formatIssues(is timeline.Issues) string {
	sorted := is.Sorted()
	var b strings.Builder
	for _, i := range sorted {
		b.WriteString(i.String())
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}
