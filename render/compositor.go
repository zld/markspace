// Package render composites track frames onto the output grid in
// dependency order and computes per-frame hashes and quality statistics.
package render

import (
	"encoding/binary"
	"hash/fnv"

	"mediapack/merr"
	"mediapack/timeline"
)

// Quality levels: a fully composited frame scores QualityValid, an
// explicit hole (e.g. an RTP loss burst) scores QualityGap. Holes are
// exported as marked frames, never disguised as valid.
const (
	QualityValid = 0.95
	QualityGap   = 0.65
)

// SourceFrame is one input frame aligned to the output grid.
type SourceFrame struct {
	Time int64  // nanoseconds on the shared timeline
	Data []byte // encoded sample payload (feeds the frame hash)
	Size int    // encoded size in bytes (feeds bitrate statistics)
}

// TrackSource is a demuxed track ready for compositing.
type TrackSource struct {
	TrackID  string
	Video    bool // audio tracks are manifest-only for compositing
	Required bool // a missing required frame turns the output frame into a hole
	Frames   []SourceFrame
}

// Frame is one composited output frame (pixel data is hashed, not kept).
type Frame struct {
	Index   int
	Time    int64
	Valid   bool
	Quality float64
	Size    int
	Hash    [32]byte
}

// Result is the compositor output.
type Result struct {
	Frames []Frame
	Stats  Stats
}

// Compositor blends video tracks onto a fixed output grid.
type Compositor struct {
	Width, Height int
	FPS           int64
	Seed          int64
}

// contentPattern deterministically derives frame pixels from the run seed,
// the track, the frame index and the source payload, so identical inputs
// reproduce identical frames and any input change changes the output.
func contentPattern(seed int64, track string, index int, data []byte, n int) []byte {
	h := fnv.New64a()
	h.Write([]byte(track))
	var b [16]byte
	binary.LittleEndian.PutUint64(b[:8], uint64(seed))
	binary.LittleEndian.PutUint64(b[8:], uint64(index))
	h.Write(b[:])
	h.Write(data)
	x := h.Sum64() | 1
	buf := make([]byte, n)
	for i := 0; i < n; i += 8 {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		binary.LittleEndian.PutUint64(buf[i:], x)
	}
	return buf
}

func findFrame(frames []SourceFrame, t int64) (SourceFrame, bool) {
	// frames are sorted by time; binary search for the closest frame and
	// accept it within 1ns to absorb rational rounding on the grid
	lo, hi := 0, len(frames)-1
	best := -1
	var bestD int64
	for lo <= hi {
		mid := (lo + hi) / 2
		d := frames[mid].Time - t
		ad := d
		if ad < 0 {
			ad = -ad
		}
		if best == -1 || ad < bestD {
			best, bestD = mid, ad
		}
		switch {
		case d == 0:
			return frames[mid], true
		case d < 0:
			lo = mid + 1
		default:
			hi = mid - 1
		}
	}
	if best >= 0 && bestD <= 1 {
		return frames[best], true
	}
	return SourceFrame{}, false
}

// Render composites the half-open interval [start, end) at the configured
// frame rate. Overlapping transitions are blended in timeline order.
func (c *Compositor) Render(sources []TrackSource, transitions []timeline.Transition, start, end int64) (*Result, error) {
	if c.Width <= 0 || c.Height <= 0 || c.FPS <= 0 {
		return nil, merr.Paramf("render", "invalid compositor geometry %dx%d@%d", c.Width, c.Height, c.FPS)
	}
	count, err := timeline.FrameCount(start, end, c.FPS)
	if err != nil {
		return nil, err
	}
	npix := c.Width * c.Height
	res := &Result{Frames: make([]Frame, 0, count)}
	var videos []TrackSource
	for _, s := range sources {
		if s.Video {
			videos = append(videos, s)
		}
	}
	if len(videos) == 0 {
		return nil, merr.Corruptf("render", "", "no video source tracks")
	}
	for i := 0; i < count; i++ {
		t := timeline.FrameTime(start, c.FPS, int64(i))
		// gather required inputs; a missing one makes this frame a hole
		type hit struct {
			track TrackSource
			frame SourceFrame
		}
		var hits []hit
		missing := false
		for _, v := range videos {
			f, ok := findFrame(v.Frames, t)
			if !ok {
				if v.Required {
					missing = true
					break
				}
				continue
			}
			hits = append(hits, hit{v, f})
		}
		var fr Frame
		fr.Index = i
		fr.Time = t
		if missing || len(hits) == 0 {
			fr.Valid = false
			fr.Quality = QualityGap
			// the hole marks the lost track; surviving tracks still
			// contribute their encoded size to bitrate statistics
			if pf, ok := findFrame(videos[0].Frames, t); ok {
				fr.Size = pf.Size
			}
			hole := contentPattern(c.Seed, "hole", i, nil, npix)
			fr.Hash = HashFrame(i, t, false, hole)
			res.Frames = append(res.Frames, fr)
			continue
		}
		// base layer: first available track
		base := contentPattern(c.Seed, hits[0].track.TrackID, i, hits[0].frame.Data, npix)
		size := hits[0].frame.Size
		// apply active transitions in timeline order
		for _, tr := range timeline.ActiveTransitions(transitions, t) {
			var dst *hit
			for j := range hits {
				if hits[j].track.TrackID == tr.ToTrack {
					dst = &hits[j]
					break
				}
			}
			if dst == nil {
				continue
			}
			over := contentPattern(c.Seed, dst.track.TrackID, i, dst.frame.Data, npix)
			w := timeline.BlendWeight(t, tr)
			k := int64(w * 256)
			for p := range base {
				base[p] = byte((int64(base[p])*(256-k) + int64(over[p])*k) / 256)
			}
		}
		fr.Valid = true
		fr.Quality = QualityValid
		fr.Size = size
		fr.Hash = HashFrame(i, t, true, base)
		res.Frames = append(res.Frames, fr)
	}
	times := make([]int64, len(res.Frames))
	for i, f := range res.Frames {
		times[i] = f.Time
	}
	if err := timeline.CheckMonotonic("render", times); err != nil {
		return nil, err
	}
	res.Stats = Aggregate(res.Frames)
	return res, nil
}
