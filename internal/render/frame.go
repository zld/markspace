// Package render produces deterministic off-screen frame sequences. Frames
// use one fixed pixel format (RGBA), apply each track's retained crop and
// explicit rotation, and carry the master-clock tick, source track and input
// digest. Every frame's pixel bytes are hashed with SHA-256.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"mediapack/internal/clock"
	"mediapack/internal/ingest"
	"mediapack/internal/timeline"
)

// PixelFormat is the single fixed pixel format the subset exports.
const PixelFormat = "RGBA"

// LayerConfig configures one bitrate representation's output geometry.
type LayerConfig struct {
	Name    string `json:"name"`
	Bitrate int    `json:"bitrate_kbps"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}

// Frame is one exported, hash-anchored off-screen image.
type Frame struct {
	Layer        string `json:"layer"`
	Index        int    `json:"frame_index"`
	Tick         int64  `json:"tick"`
	SourceTrack  int    `json:"source_track"`
	SourceSample int    `json:"source_sample"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	PixelFormat  string `json:"pixel_format"`
	InputDigest  string `json:"input_digest"`
	Hash         string `json:"sha256"`
}

// Renderer renders layers against a fixed input digest so identical inputs
// always produce identical frame hashes.
type Renderer struct {
	inputDigest string
}

// NewRenderer builds a renderer. inputDigest is the caller-computed SHA-256
// of the canonical input summary.
func NewRenderer(inputDigest string) *Renderer {
	return &Renderer{inputDigest: inputDigest}
}

// InputDigest returns the digest bound to this renderer.
func (r *Renderer) InputDigest() string { return r.inputDigest }

// RenderLayer exports every video sample of every video segment at the
// layer's geometry. Frames are ordered by global tick ordering.
func (r *Renderer) RenderLayer(layer LayerConfig, tl *timeline.Timeline) ([]Frame, error) {
	var frames []Frame
	idx := 0
	for _, seg := range tl.VideoSegments() {
		conv, err := clock.NewConverter(seg.Track.Timescale)
		if err != nil {
			return nil, err
		}
		var srcTime int64
		for si := range seg.Track.Samples {
			tick := conv.ToTicks(srcTime)
			srcTime += int64(seg.Track.Samples[si].Delta)

			src := drawSource(seg.Track, si, r.inputDigest)
			cropped := applyCrop(src, seg.Track)
			rotated := applyRotation(cropped, seg.Track.Rotation)
			out := scaleNearest(rotated, layer.Width, layer.Height)

			sum := sha256.Sum256(out.Pix)
			frames = append(frames, Frame{
				Layer:        layer.Name,
				Index:        idx,
				Tick:         tick,
				SourceTrack:  seg.GlobalIndex,
				SourceSample: si,
				Width:        layer.Width,
				Height:       layer.Height,
				PixelFormat:  PixelFormat,
				InputDigest:  r.inputDigest,
				Hash:         hex.EncodeToString(sum[:]),
			})
			idx++
		}
	}
	return frames, nil
}

// SummaryDigest computes a stable SHA-256 over the canonical JSON of the
// descriptors that influence rendering, so the digest reflects input content
// rather than file paths, offsets or timestamps (provenance is excluded).
func SummaryDigest(tracks []*ingest.Track) (string, error) {
	type slimColor struct {
		Type      string
		Primaries uint16
		Transfer  uint16
		Matrix    uint16
		FullRange bool
	}
	type slim struct {
		Index     int
		Kind      ingest.TrackKind
		Width     uint16
		Height    uint16
		Rotation  int
		Crop      *ingest.Crop
		Colors    []slimColor
		Samples   []ingest.Sample
		Timescale uint32
	}
	view := make([]slim, 0, len(tracks))
	for _, t := range tracks {
		colors := make([]slimColor, 0, len(t.Colors))
		for _, c := range t.Colors {
			colors = append(colors, slimColor{
				Type: c.Type, Primaries: c.Primaries, Transfer: c.Transfer,
				Matrix: c.Matrix, FullRange: c.FullRange,
			})
		}
		view = append(view, slim{
			Index: t.Index, Kind: t.Kind, Width: t.Width, Height: t.Height,
			Rotation: t.Rotation, Crop: t.Crop, Colors: colors,
			Samples: t.Samples, Timescale: t.Timescale,
		})
	}
	raw, err := json.Marshal(view)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
