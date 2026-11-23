package transform

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"sort"

	"mediacore/internal/timeline"
)

// Frame is one deterministic offscreen review frame. Frames are derived only
// from parsed sample metadata and payload bytes; no decoder or graphics stack
// is involved.
type Frame struct {
	Number       int    `json:"number"`
	TrackID      uint32 `json:"track_id"`
	TrackKind    string `json:"track_kind"`
	TimelineTime int64  `json:"timeline_time"`
	Duration     int64  `json:"duration"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	// PixelSHA is the digest of the synthetic RGBA raster.
	PixelSHA string `json:"pixel_sha256"`
	// SourceSHA is the digest of the input sample bytes the frame came from.
	SourceSHA    string `json:"source_sha256"`
	SourceFile   string `json:"source_file"`
	SourceOffset int64  `json:"source_offset"`
	SourceSize   int64  `json:"source_size"`
	Fragment     int    `json:"fragment_index"`
	FragmentSeq  uint32 `json:"fragment_seq"`
	SampleIndex  int    `json:"sample_index"`
}

// FrameOptions controls rasterization. The seed fixes every pixel decision.
type FrameOptions struct {
	Seed      int64
	Width     int
	Height    int
	Timescale uint32
}

// FrameSet is the exported frame sequence plus its root digest.
type FrameSet struct {
	Seed      int64   `json:"seed"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	Timescale uint32  `json:"timescale"`
	Frames    []Frame `json:"frames"`
	Digest    string  `json:"sha256"`
}

// BuildFrames generates a frame for each repaired sample in timeline order.
// The same metadata, payload and options always yield identical digests.
func BuildFrames(samples []timeline.Sample, payloads PayloadSource, opts FrameOptions) (*FrameSet, error) {
	if opts.Width <= 0 {
		opts.Width = 64
	}
	if opts.Height <= 0 {
		opts.Height = 36
	}
	sorted := append([]timeline.Sample(nil), samples...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].TimelineTime != sorted[j].TimelineTime {
			return sorted[i].TimelineTime < sorted[j].TimelineTime
		}
		if sorted[i].TrackID != sorted[j].TrackID {
			return sorted[i].TrackID < sorted[j].TrackID
		}
		if sorted[i].FragmentIndex != sorted[j].FragmentIndex {
			return sorted[i].FragmentIndex < sorted[j].FragmentIndex
		}
		return sorted[i].SampleIndex < sorted[j].SampleIndex
	})
	set := &FrameSet{Seed: opts.Seed, Width: opts.Width, Height: opts.Height, Timescale: opts.Timescale}
	h := sha256.New()
	for i, s := range sorted {
		raw, err := payloads.SampleBytes(s)
		if err != nil {
			return nil, fmt.Errorf("frame %d: %w", i, err)
		}
		pixels, err := rasterize(raw, s, opts)
		if err != nil {
			return nil, err
		}
		f := Frame{
			Number:       i,
			TrackID:      s.TrackID,
			TrackKind:    s.TrackKind,
			TimelineTime: s.TimelineTime,
			Duration:     s.TimelineDuration,
			Width:        opts.Width,
			Height:       opts.Height,
			PixelSHA:     pixels,
			SourceSHA:    digestBytes(raw),
			SourceFile:   s.File,
			SourceOffset: s.FileOffset,
			SourceSize:   s.ByteLength,
			Fragment:     s.FragmentIndex,
			FragmentSeq:  s.FragmentSeq,
			SampleIndex:  s.SampleIndex,
		}
		set.Frames = append(set.Frames, f)
		writeFrameDigest(h, f)
	}
	set.Digest = fmt.Sprintf("%x", h.Sum(nil))
	return set, nil
}

// DigestFrames recomputes the frame-set root digest from exported frames. It
// is the inverse of the digest written by BuildFrames, so an exported file can
// be verified without the producing process.
func DigestFrames(frames []Frame) string {
	h := sha256.New()
	for _, f := range frames {
		writeFrameDigest(h, f)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// PayloadSource resolves the bytes of a sample from its source file.
type PayloadSource interface {
	SampleBytes(timeline.Sample) ([]byte, error)
}

// digestBytes hashes a byte slice.
func digestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:])
}

// writeFrameDigest makes the root digest depend on the exact frame identity,
// including the source offsets, so a shifted sample changes the result.
func writeFrameDigest(h hash.Hash, f Frame) {
	var buf [16]byte
	binary.BigEndian.PutUint32(buf[:], uint32(f.Number))
	h.Write(buf[:4])
	binary.BigEndian.PutUint32(buf[:], f.TrackID)
	h.Write(buf[:4])
	binary.BigEndian.PutUint64(buf[:], uint64(f.TimelineTime))
	h.Write(buf[:8])
	binary.BigEndian.PutUint64(buf[:], uint64(f.SourceOffset))
	h.Write(buf[:8])
	binary.BigEndian.PutUint64(buf[:], uint64(f.SourceSize))
	h.Write(buf[:8])
	h.Write([]byte(f.PixelSHA))
	h.Write([]byte(f.SourceSHA))
}

// rasterize produces the digest of a deterministic RGBA raster. Pixels depend
// only on the seed, the sample metadata and the source bytes.
func rasterize(raw []byte, s timeline.Sample, opts FrameOptions) (string, error) {
	w, hgt := opts.Width, opts.Height
	seeded := sha256.New()
	var head [32]byte
	binary.BigEndian.PutUint64(head[0:], uint64(opts.Seed))
	binary.BigEndian.PutUint32(head[8:], s.TrackID)
	binary.BigEndian.PutUint64(head[12:], uint64(s.TimelineTime))
	binary.BigEndian.PutUint32(head[20:], uint32(s.FragmentSeq))
	binary.BigEndian.PutUint32(head[24:], uint32(s.SampleIndex))
	binary.BigEndian.PutUint32(head[28:], uint32(w)<<16|uint32(hgt))
	seeded.Write(head[:])
	seeded.Write(raw)
	tile := seeded.Sum(nil)

	pixels := sha256.New()
	var px [4]byte
	for y := 0; y < hgt; y++ {
		for x := 0; x < w; x++ {
			idx := (y*w + x) % len(tile)
			px[0] = tile[idx] ^ byte(x)
			px[1] = tile[(idx+7)%len(tile)] ^ byte(y)
			px[2] = tile[(idx+13)%len(tile)] ^ byte(s.TrackID)
			px[3] = 0xff
			pixels.Write(px[:])
		}
	}
	return fmt.Sprintf("%x", pixels.Sum(nil)), nil
}
