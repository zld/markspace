package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"

	"avcheck/internal/media"
	"avcheck/internal/timeline"
)

// SegmentRecord summarizes one exported segment.
type SegmentRecord struct {
	Index        int    `json:"index"`
	Frames       int    `json:"frames"`
	FirstIndex   uint64 `json:"first_index"`
	LastIndex    uint64 `json:"last_index"`
	FirstTime    string `json:"first_time"` // rational seconds
	LastTime     string `json:"last_time"`
	Recovered    int    `json:"recovered"`
	Placeholders int    `json:"placeholders"`
	SHA256       string `json:"sha256"`
}

// Manifest is the deterministic export summary; rerunning the same input
// must produce an identical manifest.
type Manifest struct {
	Version  int             `json:"version"`
	Tracks   int             `json:"tracks"`
	Frames   int             `json:"frames"`
	Segments []SegmentRecord `json:"segments"`
	Summary  string          `json:"summary"` // sha256 over segment hashes
}

// Renderer turns an output frame into an image; nil means placeholder.
type Renderer func(of timeline.OutputFrame) *image.RGBA

// Export writes PNG frames and manifest.json into dir, which is created.
// segFrames is the fixed segment size; the last segment may be shorter.
func Export(dir string, tracks int, outs []timeline.OutputFrame, segFrames int, render Renderer) (*Manifest, error) {
	if segFrames <= 0 {
		return nil, media.NewError(media.KindRange, 0, 0, -1, "segment size %d", segFrames)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	m := &Manifest{Version: 1, Tracks: tracks, Frames: len(outs)}
	var summary [][]byte
	for segStart := 0; segStart < len(outs) || (segStart == 0 && len(outs) == 0); segStart += segFrames {
		end := segStart + segFrames
		if end > len(outs) {
			end = len(outs)
		}
		rec := SegmentRecord{Index: len(m.Segments)}
		var pngs [][]byte
		for i := segStart; i < end; i++ {
			of := outs[i]
			img := render(of)
			if img == nil {
				return nil, media.NewError(media.KindState, 0, of.Index, -1, "renderer returned nil image")
			}
			var buf bytes.Buffer
			if err := png.Encode(&buf, img); err != nil {
				return nil, err
			}
			name := filepath.Join(dir, fmt.Sprintf("frame_%05d.png", i))
			if err := os.WriteFile(name, buf.Bytes(), 0o644); err != nil {
				return nil, err
			}
			pngs = append(pngs, buf.Bytes())
			rec.Frames++
			if i == segStart {
				rec.FirstIndex, rec.FirstTime = of.Index, of.Time.String()
			}
			rec.LastIndex, rec.LastTime = of.Index, of.Time.String()
			for _, s := range of.Sources {
				switch s.Frame {
				case media.StateRecovered:
					rec.Recovered++
				case media.StatePlaceholder:
					rec.Placeholders++
				}
			}
		}
		sum := SumSegment(pngs)
		rec.SHA256 = Hex(sum)
		summary = append(summary, sum[:])
		m.Segments = append(m.Segments, rec)
		if len(outs) == 0 {
			break
		}
	}
	m.Summary = Hex(SumSegment(summary))
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644); err != nil {
		return nil, err
	}
	return m, nil
}

// LoadManifest reads a manifest.json from dir.
func LoadManifest(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, media.NewError(media.KindCorrupt, 0, 0, -1, "manifest: %v", err)
	}
	return &m, nil
}

// VerifySegments recomputes per-segment hashes from the PNGs on disk.
func VerifySegments(dir string, m *Manifest) (matched int, err error) {
	frameAt := 0
	for si := range m.Segments {
		rec := &m.Segments[si]
		var paths []string
		for k := 0; k < rec.Frames; k++ {
			paths = append(paths, filepath.Join(dir, fmt.Sprintf("frame_%05d.png", frameAt+k)))
		}
		frameAt += rec.Frames
		sum, err := SumFiles(paths)
		if err != nil {
			return matched, err
		}
		if Hex(sum) == rec.SHA256 {
			matched++
		}
	}
	return matched, nil
}
