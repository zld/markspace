// Package render produces deterministic, off-screen frame records for
// sampling points taken from the unified timeline. It never fabricates a
// frame: if the sample payload cannot be read or is too short, the record
// carries the classified error instead of a pixel hash.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"

	"mediaaudit/internal/model"
)

// SampleRef is the subset of mp4.Sample the renderer needs.
type SampleRef struct {
	Index    int
	Offset   uint64
	Size     uint32
	Start    model.Rat
	Duration model.Rat
	Keyframe bool
	Origin   string
}

// FrameRecord is the auditable output for one sampling point.
type FrameRecord struct {
	Point      model.Rat    `json:"point"`
	SampleIdx  int          `json:"sample_idx"`
	Keyframe   bool         `json:"keyframe"`
	Origin     string       `json:"origin"`
	PixelHash  string       `json:"pixel_hash,omitempty"`
	ByteHash   string       `json:"byte_hash,omitempty"`
	Width      int          `json:"width"`
	Height     int          `json:"height"`
	PayloadLen uint32       `json:"payload_len"`
	Error      *model.Error `json:"error,omitempty"`
}

// Ok reports whether the record contains a real frame.
func (r FrameRecord) Ok() bool { return r.Error == nil && r.PixelHash != "" }

// Options configure rendering.
type Options struct {
	// Interval is the sampling cadence in seconds (configurable).
	Interval model.Rat
	// Start is the first sampling point; the timeline start is used when Zero.
	Start model.Rat
	// End is the exclusive upper bound; the timeline end is used when Zero.
	End model.Rat
	// Width and Height describe the synthetic raster that pixel hashes are
	// computed over. Payload bytes are expanded into this raster so hashes are
	// comparable across runs and independent of decode order.
	Width, Height int
	// MaxPoints caps the number of sampling points generated for one window,
	// bounding memory per chunk. Zero uses a high safety ceiling.
	MaxPoints int
}

// DefaultOptions returns the documented defaults.
func DefaultOptions(interval model.Rat) Options {
	return Options{Interval: interval, Width: 16, Height: 16, MaxPoints: 1_000_000}
}

// SamplingPoints returns the sorted, de-duplicated points that fall inside the
// [start,end) window of the given timeline items. Points come from normalised
// timeline spans, never from raw source times, so re-running with the same
// inputs yields the same list.
func SamplingPoints(items []model.Span, opt Options) []model.Rat {
	if opt.Interval.Sign() <= 0 {
		return nil
	}
	start, end := opt.Start, opt.End
	if start.IsZero() && end.IsZero() && len(items) > 0 {
		start = items[0].Start
		end = items[0].End
		for _, it := range items[1:] {
			if it.Start.Cmp(start) < 0 {
				start = it.Start
			}
			if it.End.Cmp(end) > 0 {
				end = it.End
			}
		}
	}
	if end.Cmp(start) <= 0 {
		return nil
	}
	limit := opt.MaxPoints
	if limit <= 0 {
		limit = 1_000_000
	}
	pts := make([]model.Rat, 0, 64)
	for p := start; p.Cmp(end) < 0; p = p.Add(opt.Interval) {
		pts = append(pts, p)
		// A zero or negative interval would loop forever; SamplingPoints
		// already rejects those, and this ceiling protects the caller.
		if len(pts) >= limit {
			break
		}
	}
	return pts
}

// FindSample returns the index of the last sample whose start is at or before
// t. The sample list must be in presentation order and its durations are not
// consulted, so a point in a gap resolves to the sample that opened the gap
// rather than inventing one. It returns -1 when t precedes every sample.
func FindSample(samples []SampleRef, t model.Rat) int {
	idx := sort.Search(len(samples), func(i int) bool {
		return samples[i].Start.Cmp(t) > 0
	}) - 1
	return idx
}

// RasterFromPayload expands (or truncates) payload bytes into a width*height
// grayscale raster. The mapping is purely functional, so identical payloads
// produce identical rasters on every platform.
func RasterFromPayload(payload []byte, width, height int) []byte {
	if width <= 0 || height <= 0 || len(payload) == 0 {
		return nil
	}
	out := make([]byte, width*height)
	for i := range out {
		out[i] = payload[i%len(payload)]
	}
	return out
}

// PixelHash hashes the raster. The dimensions are mixed in so rasters of
// different shapes cannot collide. A non-positive dimension yields an empty
// hash: there is no frame to describe.
func PixelHash(raster []byte, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	h := sha256.New()
	var dims [8]byte
	dims[0] = byte(width >> 24)
	dims[1] = byte(width >> 16)
	dims[2] = byte(width >> 8)
	dims[3] = byte(width)
	dims[4] = byte(height >> 24)
	dims[5] = byte(height >> 16)
	dims[6] = byte(height >> 8)
	dims[7] = byte(height)
	h.Write(dims[:])
	h.Write(raster)
	return hex.EncodeToString(h.Sum(nil))
}

// ByteHash hashes the raw sample payload; it lets the audit distinguish a
// rendering difference from a source difference.
func ByteHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// Reader reads a sample payload by byte range.
type Reader interface {
	ReadAt(p []byte, off int64) (int, error)
}

// RenderAt produces one record for a sampling point. Read failures and short
// payloads become classified errors; the frame is never invented.
func RenderAt(r Reader, track model.Track, samples []SampleRef, t model.Rat, opt Options) FrameRecord {
	rec := FrameRecord{Point: t, Width: opt.Width, Height: opt.Height}
	idx := FindSample(samples, t)
	if idx < 0 {
		rec.Error = model.NewError(model.CodeRenderNoSample, "render", track.ID, model.InputPos{},
			"no sample covers %s s", t)
		return rec
	}
	s := samples[idx]
	rec.SampleIdx = s.Index
	rec.Keyframe = s.Keyframe
	rec.Origin = s.Origin
	rec.PayloadLen = s.Size
	if s.Size == 0 {
		rec.Error = model.NewError(model.CodeSampleRead, "render", track.ID, model.InputPos{Offset: int64(s.Offset)},
			"sample %d has zero size", s.Index)
		return rec
	}
	buf := make([]byte, s.Size)
	n, err := r.ReadAt(buf, int64(s.Offset))
	if n < int(s.Size) {
		rec.Error = model.NewError(model.CodeSampleRead, "render", track.ID,
			model.InputPos{Offset: int64(s.Offset)},
			"sample %d: short read %d of %d bytes: %v", s.Index, n, s.Size, errOrEOF(err))
		return rec
	}
	if err != nil {
		rec.Error = model.NewError(model.CodeRenderDecode, "render", track.ID,
			model.InputPos{Offset: int64(s.Offset)}, "sample %d: %v", s.Index, err)
		return rec
	}
	raster := RasterFromPayload(buf, opt.Width, opt.Height)
	rec.ByteHash = ByteHash(buf)
	rec.PixelHash = PixelHash(raster, opt.Width, opt.Height)
	return rec
}

func errOrEOF(err error) error {
	if err == nil || err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// RenderBatch renders points in order. Records for failed samples are kept so
// the audit can count them.
func RenderBatch(r Reader, track model.Track, samples []SampleRef, points []model.Rat, opt Options) []FrameRecord {
	out := make([]FrameRecord, 0, len(points))
	for _, p := range points {
		out = append(out, RenderAt(r, track, samples, p, opt))
	}
	return out
}
