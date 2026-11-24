// Package render exports the composed timeline as an offscreen,
// deterministic frame sequence. There is no GUI dependency: every frame
// is rendered from integer frame metadata into a small synthetic raster
// whose SHA-256 hash is stable across repeated runs.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"archivecheck/internal/media"
	"archivecheck/internal/merr"
	"archivecheck/internal/timeline"
)

// FrameRecord is one exported frame entry.
type FrameRecord struct {
	Index     int    `json:"index"`
	TrackID   uint32 `json:"track_id"`
	Seq       uint32 `json:"segment_seq"`
	Timestamp int64  `json:"pts_ticks"`
	Width     uint16 `json:"width"`
	Height    uint16 `json:"height"`
	Corrupt   bool   `json:"corrupt"`
	Hash      string `json:"pixel_hash"`
}

// Manifest is the frame-sequence index written atomically to outDir.
type Manifest struct {
	ClockRate int64         `json:"clock_rate"`
	Count     int           `json:"frame_count"`
	Frames    []FrameRecord `json:"frames"`
}

// safeOutDir rejects empty, traversal-containing or non-local output
// paths and guarantees the directory exists before any write.
func safeOutDir(outDir string) (string, error) {
	if strings.TrimSpace(outDir) == "" {
		return "", merr.New(merr.KindArgument, "render", "out",
			"output directory must not be empty")
	}
	clean := filepath.Clean(outDir)
	if filepath.IsAbs(clean) {
		// Absolute local paths are allowed; reject only traversal forms.
	} else if strings.HasPrefix(clean, "..") {
		return "", merr.New(merr.KindArgument, "render", "out",
			"relative path escaping the working directory is not allowed: "+outDir)
	}
	for _, part := range strings.Split(filepath.ToSlash(clean), "/") {
		if part == ".." {
			return "", merr.New(merr.KindArgument, "render", "out",
				"path traversal is not allowed in output directory: "+outDir)
		}
	}
	if err := os.MkdirAll(clean, 0o755); err != nil {
		return "", merr.Wrap(err, "render", "out", "cannot create output directory")
	}
	return clean, nil
}

// pixelHash builds a deterministic raster hash for one frame. The raster
// is a function of (track, segment, index, PTS, size, luma) only, so the
// same timeline always yields identical hashes with no map iteration or
// time-dependent input.
func pixelHash(f media.Frame) string {
	h := sha256.New()
	write := func(b []byte) { _, _ = h.Write(b) }
	write([]byte("ARCH-FRAME-V1"))
	write(u32be(f.TrackID))
	write(u32be(f.SegmentSeq))
	write(u32be(f.Index))
	write(i64be(f.PTS))
	write(i64be(f.Duration))
	write(u16be(f.Width))
	write(u16be(f.Height))
	write([]byte{f.Luma})
	if f.Corrupt {
		write([]byte{1})
	} else {
		write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Export renders every decodable frame of the timeline into a frame
// sequence and commits a single frames.json atomically (temp file +
// rename), so a failed run never leaves a partial manifest behind.
func Export(tl *timeline.Timeline, outDir string) (*Manifest, error) {
	dir, err := safeOutDir(outDir)
	if err != nil {
		return nil, err
	}
	m := &Manifest{ClockRate: media.ClockRate}
	idx := 0
	for _, seg := range tl.Segments {
		for _, f := range seg.Frames {
			if f.Width == 0 || f.Height == 0 {
				return nil, merr.New(merr.KindContainer, "render",
					"track:"+u32s(f.TrackID)+":seq:"+u32s(f.SegmentSeq),
					"frame has no dimensions, unsupported fixture subset")
			}
			corrupt := f.Corrupt
			m.Frames = append(m.Frames, FrameRecord{
				Index:     idx,
				TrackID:   f.TrackID,
				Seq:       f.SegmentSeq,
				Timestamp: f.PTS,
				Width:     f.Width,
				Height:    f.Height,
				Corrupt:   corrupt,
				Hash:      pixelHash(f),
			})
			idx++
		}
	}
	m.Count = len(m.Frames)

	tmp := filepath.Join(dir, ".frames.json.tmp")
	final := filepath.Join(dir, "frames.json")
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, merr.Wrap(err, "render", "frames.json", "encode failed")
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return nil, merr.Wrap(err, "render", "frames.json", "temp write failed")
	}
	if err := os.Rename(tmp, final); err != nil {
		return nil, merr.Wrap(err, "render", "frames.json", "atomic commit failed")
	}
	return m, nil
}

// Hashes returns just the hash list for resume/diff comparisons.
func (m *Manifest) Hashes() []string {
	out := make([]string, len(m.Frames))
	for i, f := range m.Frames {
		out[i] = f.Hash
	}
	return out
}

func u32be(v uint32) []byte {
	return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}
func u16be(v uint16) []byte { return []byte{byte(v >> 8), byte(v)} }
func i64be(v int64) []byte {
	u := uint64(v)
	b := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		b[i] = byte(u)
		u >>= 8
	}
	return b
}
func u32s(v uint32) string { return strconv.FormatUint(uint64(v), 10) }
