package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/zld/mediapipe/index"
	"github.com/zld/mediapipe/media"
)

// LayerInput binds one representation layer to its source file and index.
type LayerInput struct {
	Name  string            `json:"name"`
	Path  string            `json:"path"`
	Index *index.Index      `json:"-"`
	Track *index.TrackIndex `json:"-"`
}

// SegmentOutput is one written segment.
type SegmentOutput struct {
	Number    int           `json:"number"`
	StartUS   int64         `json:"start_us"`
	EndUS     int64         `json:"end_us"`
	Path      string        `json:"path"`
	SHA256    string        `json:"sha256"`
	Bytes     int64         `json:"bytes"`
	Samples   int           `json:"samples"`
	Layer     string        `json:"layer"`
	ElapsedUS int64         `json:"elapsed_us"`
	Issues    []media.Issue `json:"issues"`
}

// SegmentConfig controls streaming execution.
type SegmentConfig struct {
	WorkDir      string // absolute output directory
	Segment      media.Time
	MaxDeviation media.Time
	// MaxBytesPerSegment bounds the read buffer; a segment that would exceed
	// it fails instead of growing without limit.
	MaxBytesPerSegment int64
}

// DefaultMaxBytesPerSegment bounds a single output segment's payload.
func DefaultMaxBytesPerSegment() int64 { return 256 << 20 }

func (c SegmentConfig) validate() error {
	if c.WorkDir == "" {
		return media.NewError(media.KindArgument, "", "work directory is required")
	}
	if c.Segment <= 0 {
		return media.NewError(media.KindArgument, "", "segment duration must be positive")
	}
	if c.MaxBytesPerSegment <= 0 {
		return media.NewError(media.KindArgument, "", "segment byte limit must be positive")
	}
	return nil
}

// Execute writes the payload bytes for every planned segment of every layer.
//
// Execution is streaming: each segment is read in bounded chunks from the
// source file, its bytes and digest are written to a same-directory temp file,
// and the file is atomically renamed only after the digest is known. The
// returned outputs are keyed by (layer, segment number) and sorted in a stable
// order, so identical inputs and parameters produce identical results.
func Execute(layers []LayerInput, plan []PlannedSegment, cfg SegmentConfig) ([]SegmentOutput, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return nil, media.WrapError(media.KindIO, cfg.WorkDir, err)
	}
	var out []SegmentOutput
	for _, layer := range layers {
		if layer.Index == nil || layer.Track == nil {
			return nil, media.NewError(media.KindUnsupported, layer.Name, "layer has no index")
		}
		if layer.Track.Timescale == 0 {
			return nil, media.NewError(media.KindUnsupported, layer.Name, "layer timescale is zero")
		}
		for _, seg := range plan {
			started := time.Now()
			so, err := writeSegment(layer, seg, cfg)
			if err != nil {
				return nil, err
			}
			so.ElapsedUS = time.Since(started).Microseconds()
			out = append(out, so)
		}
	}
	return out, nil
}

func writeSegment(layer LayerInput, seg PlannedSegment, cfg SegmentConfig) (SegmentOutput, error) {
	src, err := os.Open(layer.Path)
	if err != nil {
		return SegmentOutput{}, media.WrapError(media.KindIO, layer.Path, err)
	}
	defer src.Close()

	// Select samples whose sample start falls inside the segment window.
	// Samples are kept whole; a segment therefore covers [start,end) on the
	// layer's own sample grid, which is aligned to the shared boundaries.
	var samples []media.Sample
	for _, s := range layer.Track.Samples {
		if int64(s.StartUS) >= seg.StartUS && int64(s.StartUS) < seg.EndUS {
			samples = append(samples, s)
		}
	}
	var total int64
	for _, s := range samples {
		total += s.Size
	}
	if total > cfg.MaxBytesPerSegment {
		return SegmentOutput{}, media.NewError(media.KindLimit, layer.Name,
			"segment %d payload %d bytes exceeds limit %d", seg.Number, total, cfg.MaxBytesPerSegment)
	}

	name := fmt.Sprintf("%s-seg%03d.bin", sanitize(layer.Name), seg.Number)
	dest := filepath.Join(cfg.WorkDir, name)
	if !withinDir(cfg.WorkDir, dest) {
		return SegmentOutput{}, media.NewError(media.KindArgument, dest, "output escapes work directory")
	}

	f, err := os.CreateTemp(cfg.WorkDir, ".tmp-seg-*")
	if err != nil {
		return SegmentOutput{}, media.WrapError(media.KindIO, cfg.WorkDir, err)
	}
	tmp := f.Name()
	cleanup := func() {
		f.Close()
		_ = os.Remove(tmp)
	}

	h := sha256.New()
	var written int64
	buf := make([]byte, 0, 1<<20)
	var issues []media.Issue
	for _, s := range samples {
		if s.Offset < 0 || s.Size < 0 {
			cleanup()
			return SegmentOutput{}, media.NewError(media.KindCorruptBox, layer.Name,
				"sample %d has invalid offset/size", s.Number)
		}
		if _, err := src.Seek(s.Offset, io.SeekStart); err != nil {
			cleanup()
			return SegmentOutput{}, media.WrapError(media.KindIO, layer.Path, err)
		}
		need := s.Size
		if int64(cap(buf)) < need {
			buf = make([]byte, need)
		}
		buf = buf[:need]
		if _, err := io.ReadFull(src, buf); err != nil {
			cleanup()
			// A short read means the sample extends past the end of the file.
			issues = append(issues, media.Issue{Kind: media.KindTruncatedInput, Severity: media.SeverityError,
				Detail: fmt.Sprintf("sample %d extends past end of file", s.Number), At: s.Number, TimeUS: s.StartUS})
			return SegmentOutput{}, media.NewError(media.KindTruncatedInput, layer.Name,
				"sample %d read failed", s.Number)
		}
		if _, err := h.Write(buf); err != nil {
			cleanup()
			return SegmentOutput{}, media.WrapError(media.KindIO, tmp, err)
		}
		if _, err := f.Write(buf); err != nil {
			cleanup()
			return SegmentOutput{}, media.WrapError(media.KindIO, tmp, err)
		}
		written += need
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if err := f.Sync(); err != nil {
		cleanup()
		return SegmentOutput{}, media.WrapError(media.KindIO, tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return SegmentOutput{}, media.WrapError(media.KindIO, tmp, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return SegmentOutput{}, media.WrapError(media.KindIO, dest, err)
	}
	return SegmentOutput{
		Number: seg.Number, StartUS: seg.StartUS, EndUS: seg.EndUS,
		Path: name, SHA256: sum, Bytes: written, Samples: len(samples), Layer: layer.Name,
		Issues: issues,
	}, nil
}

func sanitize(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "layer"
	}
	return string(out)
}

// withinDir reports whether path is inside dir after cleaning. Both paths are
// expected to be absolute.
func withinDir(dir, path string) bool {
	dirAbs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(dirAbs, pathAbs)
	if err != nil {
		return false
	}
	return rel != "." && !filepath.IsAbs(rel) && !startsUp(rel)
}

func startsUp(rel string) bool {
	return len(rel) >= 2 && rel[0] == '.' && rel[1] == '.'
}
