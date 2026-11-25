// Package render exports deterministic offscreen frame artifacts.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"medpipe/internal/fault"
	"medpipe/internal/ingest"
	"medpipe/internal/timeline"
)

// FrameArtifact is one exported PPM and its content hash.
type FrameArtifact struct {
	Sequence    int      `json:"sequence"`
	TrackID     uint16   `json:"track_id"`
	TimestampNS int64    `json:"timestamp_ns"`
	Path        string   `json:"path,omitempty"`
	ByteCount   int      `json:"byte_count"`
	SHA256      [32]byte `json:"sha256"`
}

// Segment is an independently hashable exported frame fragment.
type Segment struct {
	Index    int      `json:"index"`
	FrameSeq int      `json:"frame_seq"`
	Hash     [32]byte `json:"sha256"`
	PPMBytes int      `json:"ppm_bytes"`
}

// Manifest is the deterministic export result.
type Manifest struct {
	Width         int             `json:"-"`
	FrameCount    int             `json:"frame_count"`
	Frames        []FrameArtifact `json:"frames"`
	Segments      []Segment       `json:"segments"`
	SegmentSHA256 [32]byte        `json:"segment_sha256"`
}

// EncodePPM encodes validated RGB24 pixels as binary P6 PPM. The header has no
// comments or platform-specific data, making output byte-for-byte stable.
func EncodePPM(f *ingest.Frame) ([]byte, error) {
	if f == nil {
		return nil, fmt.Errorf("nil frame")
	}
	if f.Width <= 0 || f.Height <= 0 {
		return nil, fmt.Errorf("invalid dimensions %dx%d", f.Width, f.Height)
	}
	want := f.Width * f.Height * 3
	if f.PixelFormat != ingest.PixelRGB24 || len(f.Pixels) != want {
		return nil, fmt.Errorf("invalid RGB24 pixels: format=%d len=%d want=%d", f.PixelFormat, len(f.Pixels), want)
	}
	header := fmt.Sprintf("P6\n%d %d\n255\n", f.Width, f.Height)
	out := make([]byte, 0, len(header)+len(f.Pixels))
	out = append(out, header...)
	out = append(out, f.Pixels...)
	return out, nil
}

// Export writes timeline frames as PPM files when dir is non-empty and builds
// per-segment hashes plus one chained whole-run hash.
func Export(frames []timeline.Frame, dir string) (*Manifest, error) {
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fault.New(fault.StageRender, "create_output_dir", -1, -1, err)
		}
	}
	m := &Manifest{Frames: make([]FrameArtifact, 0, len(frames)), Segments: make([]Segment, 0, len(frames))}
	chain := sha256.New()

	for i, tf := range frames {
		if tf.Frame == nil {
			continue
		}
		ppm, err := EncodePPM(tf.Frame)
		if err != nil {
			return nil, fault.New(fault.StageRender, "ppm_encode", tf.Offset, int(tf.TrackID), err)
		}
		sum := sha256.Sum256(ppm)
		name := fmt.Sprintf("segment_%05d.ppm", i+1)
		path := ""
		if dir != "" {
			path = filepath.Join(dir, name)
			if err := os.WriteFile(path, ppm, 0o644); err != nil {
				return nil, fault.New(fault.StageRender, "write_ppm", tf.Offset, int(tf.TrackID), err)
			}
		}
		artifact := FrameArtifact{
			Sequence: i + 1, TrackID: tf.TrackID, TimestampNS: tf.TimestampNS,
			Path: path, ByteCount: len(ppm), SHA256: sum,
		}
		m.Frames = append(m.Frames, artifact)
		m.Segments = append(m.Segments, Segment{Index: i + 1, FrameSeq: i + 1, Hash: sum, PPMBytes: len(ppm)})

		fmt.Fprintf(chain, "medpipe-segment-v1 %d track=%d ns=%d bytes=%d\n", i+1, tf.TrackID, tf.TimestampNS, len(ppm))
		chain.Write(sum[:])
	}
	copy(m.SegmentSHA256[:], chain.Sum(nil))
	m.FrameCount = len(m.Frames)
	return m, nil
}

// SegmentSHA256Hex returns the stable whole-manifest hash.
func (m *Manifest) SegmentSHA256Hex() string {
	if m == nil {
		return ""
	}
	return hex.EncodeToString(m.SegmentSHA256[:])
}
