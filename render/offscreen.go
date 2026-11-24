// Package render performs headless frame extraction: frames are sampled at
// fixed points on the common time base and recorded as reproducible SHA-256
// hashes. No display, browser or camera/microphone dependency exists.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"mediacheck/ebml"
	"mediacheck/timeline"
)

// Missing reasons reported in place of a hash.
const (
	ReasonNoBlock     = "no frame block covering sample time"
	ReasonCheckFailed = "source block failed cluster CRC"
	ReasonTruncated   = "input truncated before frame was delivered"
)

// FrameHash is one reviewer-facing frame record.
type FrameHash struct {
	Index    int    `json:"index"`
	SampleNs int64  `json:"sample_time_ns"`
	Offset   int64  `json:"offset"`
	Hash     string `json:"hash,omitempty"`
	Missing  bool   `json:"missing"`
	Reason   string `json:"reason,omitempty"`
}

// VideoSource groups the blocks and geometry the renderer needs.
type VideoSource struct {
	TrackNumber uint32
	Width       int
	Height      int
	// Blocks are the video track's payload-bearing scan blocks.
	Blocks []*ebml.BlockInfo
	// TruncatedAt is the scan-level truncation offset, -1 when intact.
	TruncatedAt int64
	// ExpectedFrames optionally declares the programme's planned frame count.
	// When set with a truncation, the sampler reaches past retained blocks so
	// the missing tail is recorded instead of silently shortened.
	ExpectedFrames int
	// FrameRate is the declared video tick rate used for expected timing.
	FrameRate int64
}

// Result is the deterministic frame sequence for one run.
type Result struct {
	Frames []FrameHash
}

// Export samples frameCount fixed-time frames across the video duration.
// Sampling positions derive solely from the count and duration, so identical
// inputs and parameters always yield identical hashes.
func Export(src VideoSource, frameCount int) (*Result, error) {
	if frameCount <= 0 {
		return nil, timeline.New(timeline.CatConfig, "frame count must be positive")
	}
	if len(src.Blocks) == 0 {
		return nil, timeline.New(timeline.CatConfig, "video track has no blocks")
	}
	blocks := append([]*ebml.BlockInfo(nil), src.Blocks...)
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].TimeNs < blocks[j].TimeNs })
	first := blocks[0].TimeNs
	last := blocks[len(blocks)-1].TimeNs
	if last < first {
		return nil, timeline.New(timeline.CatCorrupt, "video blocks are not time-ordered")
	}
	// On truncation extend the sampling horizon to the declared expected end.
	horizon := last
	if src.TruncatedAt >= 0 && src.ExpectedFrames > 0 && src.FrameRate > 0 {
		expectedEnd, ok := timeline.NsFromUnits(int64(src.ExpectedFrames)-1, src.FrameRate)
		if ok && expectedEnd > horizon {
			horizon = expectedEnd
		}
	}
	res := &Result{Frames: make([]FrameHash, 0, frameCount)}
	for i := 0; i < frameCount; i++ {
		// Evenly spaced sample positions inclusive of both endpoints.
		sample := first
		if frameCount > 1 {
			sample = first + (horizon-first)*int64(i)/int64(frameCount-1)
		}
		res.Frames = append(res.Frames, sampleFrame(src, blocks, i, sample))
	}
	return res, nil
}

func sampleFrame(src VideoSource, blocks []*ebml.BlockInfo, index int, sample int64) FrameHash {
	fh := FrameHash{Index: index, SampleNs: sample}
	last := blocks[len(blocks)-1]
	// A sample beyond the last retained block can only exist because the
	// stream was truncated before the covering frame arrived.
	if src.TruncatedAt >= 0 && sample > last.TimeNs {
		fh.Missing, fh.Reason, fh.Offset = true, ReasonTruncated, src.TruncatedAt
		return fh
	}
	// Latest block whose timestamp is not after the sample point.
	var chosen *ebml.BlockInfo
	for _, b := range blocks {
		if b.TimeNs <= sample {
			chosen = b
		} else {
			break
		}
	}
	if chosen == nil {
		fh.Missing, fh.Reason = true, ReasonNoBlock
		return fh
	}
	fh.Offset = chosen.Offset
	if !chosen.CheckOK {
		fh.Missing, fh.Reason = true, ReasonCheckFailed
		return fh
	}
	sum := sha256.Sum256(chosen.Payload)
	fh.Hash = hex.EncodeToString(sum[:])
	return fh
}

// HashSequence returns only the hex hashes, empty string marking a missing
// frame, for compact deterministic comparison between runs.
func (r *Result) HashSequence() []string {
	out := make([]string, len(r.Frames))
	for i, f := range r.Frames {
		out[i] = f.Hash
	}
	return out
}

// MissingCount reports how many sample positions could not be hashed.
func (r *Result) MissingCount() int {
	n := 0
	for _, f := range r.Frames {
		if f.Missing {
			n++
		}
	}
	return n
}
