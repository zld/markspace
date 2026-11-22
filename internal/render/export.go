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

// FrameRecord is the per-frame provenance entry stored in the manifest.
// Times are rational strings so comparison never depends on float rounding.
type FrameRecord struct {
	Index   uint64            `json:"index"`
	File    string            `json:"file"`
	Time    string            `json:"time"`
	Tick    string            `json:"tick"` // time scaled to the clock timescale
	Primary uint32            `json:"primary_track"`
	Key     bool              `json:"key"`
	States  map[string]string `json:"states"` // track id -> clean|recovered|placeholder|degraded
	SHA256  string            `json:"sha256"`
}

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
	Keyframes    int    `json:"keyframes"`
	SHA256       string `json:"sha256"`
}

// Manifest is the deterministic export summary; rerunning the same input
// must produce an identical manifest.
type Manifest struct {
	Version  int             `json:"version"`
	Tracks   int             `json:"tracks"`
	Frames   int             `json:"frames"`
	Segments []SegmentRecord `json:"segments"`
	Records  []FrameRecord   `json:"records"`
	Summary  string          `json:"summary"` // sha256 over segment hashes
	Digest   string          `json:"digest"`  // sha256 over the full manifest body
}

// Renderer turns an output frame into an image; nil means placeholder.
type Renderer func(of timeline.OutputFrame) *image.RGBA

// Options configures one export run.
type Options struct {
	SegFrames int
	Clock     media.Clock // used for the per-frame tick field
}

// Export writes PNG frames and manifest.json into dir, which is created.
// segFrames is the fixed segment size; the last segment may be shorter.
func Export(dir string, tracks int, outs []timeline.OutputFrame, segFrames int, render Renderer) (*Manifest, error) {
	return ExportWith(dir, tracks, outs, Options{SegFrames: segFrames}, render)
}

// ExportWith is Export with explicit options.
func ExportWith(dir string, tracks int, outs []timeline.OutputFrame, opt Options, render Renderer) (*Manifest, error) {
	if opt.SegFrames <= 0 {
		return nil, media.NewError(media.KindRange, 0, 0, -1, "segment size %d", opt.SegFrames)
	}
	if render == nil {
		return nil, media.NewError(media.KindState, 0, 0, -1, "nil renderer")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	segFrames := opt.SegFrames
	m := &Manifest{Version: 2, Tracks: tracks, Frames: len(outs), Records: make([]FrameRecord, 0, len(outs))}
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
			name := fmt.Sprintf("frame_%05d.png", i)
			if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644); err != nil {
				return nil, err
			}
			pngs = append(pngs, buf.Bytes())
			fr := FrameRecord{
				Index:  of.Index,
				File:   name,
				Time:   of.Time.String(),
				States: make(map[string]string, len(of.Sources)),
				SHA256: Hex(SumSegment([][]byte{buf.Bytes()})),
			}
			if of.Primary != nil {
				fr.Primary, fr.Key = of.Primary.StreamID, of.Primary.Key
			}
			if opt.Clock.Timescale > 0 {
				tk, err := opt.Clock.Ticks(of.Time)
				if err != nil {
					return nil, err
				}
				fr.Tick = fmt.Sprintf("%d", tk)
			}
			rec.Frames++
			if i == segStart {
				rec.FirstIndex, rec.FirstTime = of.Index, of.Time.String()
			}
			rec.LastIndex, rec.LastTime = of.Index, of.Time.String()
			for _, s := range of.Sources {
				fr.States[fmt.Sprintf("%d", s.TrackID)] = s.Frame.String()
				switch s.Frame {
				case media.StateRecovered:
					rec.Recovered++
				case media.StatePlaceholder:
					rec.Placeholders++
				}
			}
			if fr.Key {
				rec.Keyframes++
			}
			m.Records = append(m.Records, fr)
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
	return m, saveManifest(dir, m)
}

// saveManifest writes manifest.json with a digest over the body, so reruns
// can be compared and tampering detected without external state.
func saveManifest(dir string, m *Manifest) error {
	m.Digest = ""
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	m.Digest = Hex(SumSegment([][]byte{body}))
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644)
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
	if len(m.Records) != m.Frames {
		return nil, media.NewError(media.KindCorrupt, 0, 0, -1,
			"manifest record count %d != frames %d", len(m.Records), m.Frames)
	}
	return &m, nil
}

// ManifestDigest recomputes the digest of a loaded manifest.
func ManifestDigest(m *Manifest) string {
	c := *m
	c.Digest = ""
	body, err := json.Marshal(&c)
	if err != nil {
		return ""
	}
	return Hex(SumSegment([][]byte{body}))
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
