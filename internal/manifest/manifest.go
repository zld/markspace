// Package manifest writes and verifies the cross-checkable release manifest.
// The manifest is canonical deterministic JSON (fixed field order, no maps,
// no timestamps); a sidecar SHA-256 seals its bytes, and per-frame records on
// disk let an independent verify pass re-hash every exported frame.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"mediapack/internal/audio"
	"mediapack/internal/clock"
	"mediapack/internal/fanout"
	"mediapack/internal/model"
)

// ManifestVersion is bumped when the on-disk schema changes incompatibly.
const ManifestVersion = 1

// File names used inside the output directory.
const (
	FileManifest   = "manifest.json"
	FileChecksum   = "manifest.sha256"
	FileCheckpoint = "checkpoint.json"
	DirFrames      = "frames"
	DirLayers      = "layers"
)

// FrameEntry is the on-disk record of one exported frame.
type FrameEntry struct {
	Index        int    `json:"frame_index"`
	Tick         int64  `json:"tick"`
	SourceTrack  int    `json:"source_track"`
	SourceSample int    `json:"source_sample"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	PixelFormat  string `json:"pixel_format"`
	InputDigest  string `json:"input_digest"`
	SHA256       string `json:"sha256"`
}

// SegmentEntry records one representation segment and its gaps.
type SegmentEntry struct {
	Index      int          `json:"index"`
	TickStart  int64        `json:"tick_start"`
	TickEnd    int64        `json:"tick_end"`
	FrameStart int          `json:"frame_start"`
	FrameEnd   int          `json:"frame_end"`
	Complete   bool         `json:"complete"`
	Seqs       []uint16     `json:"seqs,omitempty"`
	Gaps       []fanout.Gap `json:"gaps,omitempty"`
}

// LayerEntry is one bitrate representation in the manifest.
type LayerEntry struct {
	Name         string         `json:"name"`
	BitrateKbps  int            `json:"bitrate_kbps"`
	Width        int            `json:"width"`
	Height       int            `json:"height"`
	SSRC         uint32         `json:"ssrc"`
	FramesPerSeg int            `json:"frames_per_segment"`
	PacketsSent  int            `json:"packets_sent"`
	Received     int            `json:"packets_received_unique"`
	Duplicates   int            `json:"duplicates"`
	Reordered    bool           `json:"reordered"`
	FirstSeq     uint16         `json:"first_seq"`
	LastSeq      uint16         `json:"last_seq"`
	Segments     []SegmentEntry `json:"segments"`
	Frames       []FrameEntry   `json:"frames"`
}

// Manifest is the sealed top-level release descriptor.
type Manifest struct {
	Version        int                  `json:"version"`
	Stage          model.Stage          `json:"stage"`
	Seed           int64                `json:"seed"`
	InputDigest    string               `json:"input_digest"`
	TicksPerSecond int64                `json:"ticks_per_second"`
	DurationTicks  int64                `json:"duration_ticks"`
	MaxAlignment   clock.AlignmentError `json:"max_alignment"`
	FrameCount     int                  `json:"frame_count"`
	Layers         []LayerEntry         `json:"layers"`
	Audio          []audio.TrackStats   `json:"audio,omitempty"`
}

// Build assembles a manifest from pipeline results without touching disk.
func Build(seed int64, stage model.Stage, inputDigest string,
	durationTicks int64, maxAlign clock.AlignmentError,
	reps []*fanout.Representation, frameByLayer map[string][]FrameEntry,
	audioStats []audio.TrackStats) *Manifest {
	m := &Manifest{
		Version:        ManifestVersion,
		Stage:          stage,
		Seed:           seed,
		InputDigest:    inputDigest,
		TicksPerSecond: clock.TicksPerSecond,
		DurationTicks:  durationTicks,
		MaxAlignment:   maxAlign,
		Audio:          audioStats,
	}
	for _, rep := range reps {
		le := LayerEntry{
			Name: rep.Layer.Name, BitrateKbps: rep.Layer.BitrateKbps,
			Width: rep.Layer.Width, Height: rep.Layer.Height,
			SSRC: rep.Layer.SSRC, FramesPerSeg: rep.Layer.FramesPerSeg,
			PacketsSent: rep.Sent, Received: rep.Received,
			Duplicates: rep.Duplicates, Reordered: rep.Reordered,
			FirstSeq: rep.FirstSeq, LastSeq: rep.LastSeq,
			Frames: frameByLayer[rep.Layer.Name],
		}
		for _, s := range rep.Segments {
			le.Segments = append(le.Segments, SegmentEntry{
				Index: s.Index, TickStart: s.TickStart, TickEnd: s.TickEnd,
				FrameStart: s.FrameStart, FrameEnd: s.FrameEnd,
				Complete: s.Complete, Seqs: s.Seqs, Gaps: s.Gaps,
			})
		}
		m.Layers = append(m.Layers, le)
		m.FrameCount += len(le.Frames)
	}
	return m
}

// CanonicalBytes renders the manifest in its stable on-disk form.
func CanonicalBytes(m *Manifest) ([]byte, error) {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// HashBytes returns the SHA-256 hex digest of the canonical manifest bytes.
func HashBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
