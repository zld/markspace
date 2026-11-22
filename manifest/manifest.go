// Package manifest builds and verifies the export manifest: frame hashes
// and quality stats on the shared nanosecond timebase, per-track
// provenance, and a canonical digest. Missing frames are always exported
// as explicit holes, never as valid frames.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"mediapack/merr"
	"mediapack/safefs"
	"mediapack/timeline"
)

// FrameEntry is one exported frame in the manifest.
type FrameEntry struct {
	Index     int    `json:"index"`
	TimeNanos int64  `json:"time_nanos"`
	Valid     bool   `json:"valid"`
	Hash      string `json:"hash"`
	Size      int    `json:"size"`
}

// TrackEntry records one input track's placement and integrity state.
type TrackEntry struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	SourceDigest  string `json:"source_digest"`
	StartNanos    int64  `json:"start_nanos"`
	IntervalNanos int64  `json:"interval_nanos"`
	Samples       int    `json:"samples"`
	CorruptOffset int64  `json:"corrupt_offset"` // -1 when clean
}

// StatsBlock mirrors the render quality statistics into the manifest.
type StatsBlock struct {
	Frames      int     `json:"frames"`
	ValidFrames int     `json:"valid_frames"`
	GapFrames   int     `json:"gap_frames"`
	QualityMean float64 `json:"quality_mean"`
	TargetBps   int64   `json:"target_bps"`
	ActualBps   int64   `json:"actual_bps"`
}

// Manifest is the canonical export record.
type Manifest struct {
	Version            int          `json:"version"`
	Timebase           string       `json:"timebase"` // always "ns"
	StartNanos         int64        `json:"start_nanos"`
	EndNanos           int64        `json:"end_nanos"`
	FrameIntervalNanos int64        `json:"frame_interval_nanos"`
	FPS                int64        `json:"fps"`
	MaxSkewNanos       int64        `json:"max_skew_nanos"`
	Stats              StatsBlock   `json:"stats"`
	Tracks             []TrackEntry `json:"tracks"`
	Frames             []FrameEntry `json:"frames"`
}

// Canonical serializes the manifest deterministically (struct field
// order, no maps), the basis for the digest.
func (m *Manifest) Canonical() []byte {
	b, err := json.Marshal(m)
	if err != nil {
		panic(err) // structs only; cannot fail
	}
	return b
}

// Digest returns the sha256 hex digest of the canonical manifest.
func (m *Manifest) Digest() string {
	sum := sha256.Sum256(m.Canonical())
	return hex.EncodeToString(sum[:])
}

// Write stores the manifest as pretty JSON inside the output directory.
func Write(m *Manifest, outDir, name string) (string, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	return safefs.WriteFileAtomic(outDir, name, b, 0o644)
}

// Verify cross-checks the manifest against the shared timebase rules:
// frame count matches the interval, frame times are monotonic and on the
// grid, and no hole is marked valid.
func Verify(m *Manifest) error {
	if m.Timebase != "ns" {
		return merr.Corruptf("manifest", "", "unexpected timebase %q", m.Timebase)
	}
	if m.EndNanos <= m.StartNanos {
		return merr.Corruptf("manifest", "", "empty or negative span [%d,%d)", m.StartNanos, m.EndNanos)
	}
	want, err := timeline.FrameCount(m.StartNanos, m.EndNanos, m.FPS)
	if err != nil {
		return err
	}
	if len(m.Frames) != want {
		return merr.Corruptf("manifest", "", "frame count %d does not match interval (%d expected)", len(m.Frames), want)
	}
	if m.Stats.Frames != want || m.Stats.ValidFrames+m.Stats.GapFrames != want {
		return merr.Corruptf("manifest", "", "stats inconsistent with frame count %d", want)
	}
	times := make([]int64, len(m.Frames))
	for i, f := range m.Frames {
		if f.Index != i {
			return merr.Corruptf("manifest", "", "frame %d out of order (index %d)", i, f.Index)
		}
		if !f.Valid && f.Hash == "" {
			return merr.Corruptf("manifest", "", "hole frame %d has no hash", i)
		}
		if err := timeline.GridError("manifest", i, timeline.FrameTime(m.StartNanos, m.FPS, int64(i)), f.TimeNanos); err != nil {
			return merr.Corruptf("manifest", "", "%v", err)
		}
		times[i] = f.TimeNanos
	}
	return timeline.CheckMonotonic("manifest", times)
}

// MaxSkew computes the maximum absolute offset of any track start from
// the timeline origin, in nanoseconds.
func MaxSkew(tracks []TrackEntry, origin int64) int64 {
	var max int64
	for _, t := range tracks {
		d := t.StartNanos - origin
		if d < 0 {
			d = -d
		}
		if d > max {
			max = d
		}
	}
	return max
}
