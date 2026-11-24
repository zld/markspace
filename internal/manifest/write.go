// Package manifest writes the frame manifest and the JSON run summary. Both
// are deterministic: map iteration is avoided, timestamps are explicitly
// supplied, and hashes cover exactly the bytes written.
package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// FrameEntry ties one rendered PNG to the timestamp and transform that made it.
type FrameEntry struct {
	TrackID       string `json:"track_id"`
	TimestampNS   int64  `json:"timestamp_ns"`
	FrameIndex    int    `json:"frame_index"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	Crop          [4]int `json:"crop"`
	Rotation      int    `json:"rotation"`
	ColorResolved bool   `json:"color_resolved"`
	File          string `json:"file"`
	SHA256        string `json:"sha256"`
}

// LayerEntry records the recovery outcome of one representation layer.
type LayerEntry struct {
	Name         string  `json:"name"`
	TargetKbps   int     `json:"target_kbps"`
	ActualKbps   float64 `json:"actual_kbps"`
	DeviationPct float64 `json:"deviation_pct"`
	Frames       int     `json:"frames"`
	Duplicates   int     `json:"duplicates"`
	Reordered    int     `json:"reordered"`
	Lost         int     `json:"lost"`
	Gaps         int     `json:"gaps"`
	Isolated     bool    `json:"isolated"`
	Reason       string  `json:"reason,omitempty"`
	PSNRDb       float64 `json:"psnr_db"`
	FrameSHA256  string  `json:"frame_sha256"`
}

// Manifest is the on-disk frame list plus provenance.
type Manifest struct {
	Version         int          `json:"version"`
	TimelineNS      int64        `json:"timeline_ns"`
	ManifestAlignNS int64        `json:"manifest_alignment_ns"`
	Frames          []FrameEntry `json:"frames"`
	Layers          []LayerEntry `json:"layers"`
	Warnings        []string     `json:"warnings,omitempty"`
}

// Build assembles a manifest, sorting frames by timestamp then track for a
// stable byte-for-byte result.
func Build(timelineNS, alignNS int64, frames []FrameEntry, layers []LayerEntry, warnings []string) *Manifest {
	sorted := append([]FrameEntry(nil), frames...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].TimestampNS != sorted[j].TimestampNS {
			return sorted[i].TimestampNS < sorted[j].TimestampNS
		}
		if sorted[i].TrackID != sorted[j].TrackID {
			return sorted[i].TrackID < sorted[j].TrackID
		}
		return sorted[i].FrameIndex < sorted[j].FrameIndex
	})
	sortedLayers := append([]LayerEntry(nil), layers...)
	sort.SliceStable(sortedLayers, func(i, j int) bool { return sortedLayers[i].Name < sortedLayers[j].Name })
	return &Manifest{
		Version:         1,
		TimelineNS:      timelineNS,
		ManifestAlignNS: alignNS,
		Frames:          sorted,
		Layers:          sortedLayers,
		Warnings:        warnings,
	}
}

// JSON renders the manifest with two-space indentation and a trailing newline.
func (m *Manifest) JSON() ([]byte, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// WriteFrames writes each PNG into dir and returns the manifest. Hashes are
// recomputed from the bytes written, so the manifest always matches disk.
func (m *Manifest) WriteFrames(dir string, pngs map[string][]byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create frame directory: %w", err)
	}
	for i := range m.Frames {
		f := &m.Frames[i]
		data, ok := pngs[f.File]
		if !ok {
			return fmt.Errorf("manifest references missing frame %q", f.File)
		}
		path := filepath.Join(dir, f.File)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return fmt.Errorf("write frame %s: %w", path, err)
		}
		f.SHA256 = HashSHA256(data)
	}
	return nil
}

// WriteJSON writes the manifest beside the frames.
func (m *Manifest) WriteJSON(path string) error {
	b, err := m.JSON()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// AggregateHash is a stable digest over every frame hash and its metadata,
// used as the single "manifest hash" reported to operators.
func (m *Manifest) AggregateHash() string {
	var buf []byte
	for _, f := range m.Frames {
		buf = append(buf, []byte(fmt.Sprintf("%s|%d|%d|%s|%s\n",
			f.TrackID, f.TimestampNS, f.FrameIndex, f.File, f.SHA256))...)
	}
	return HashSHA256(buf)
}

// FrameCount is the number of rendered frames.
func (m *Manifest) FrameCount() int { return len(m.Frames) }
