// Package render turns reconstructed PCM blocks into deterministic,
// off-screen segment artifacts. No GUI libraries are used: every output
// byte is a pure function of the block digest and audio statistics, so
// repeated runs reproduce identical files and hashes.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"

	"continuity/internal/media"
)

// Frame is one fixed-size off-screen frame. Pixels are 8-bit grayscale.
type Frame struct {
	Index  int
	Width  int
	Height int
	Pixels []byte
	Mean   float64
	Hash   string
}

// CanMerge reports whether two adjacent segments may be combined. Segments
// are never merged across an input gap because the missing samples make
// the boundary untrustworthy after a primary/backup source switch.
func CanMerge(a, b Segment) bool {
	if a.Gap || b.Gap {
		return false
	}
	return a.EndSample == b.StartSample
}

// Segment is the rendered artifact for one source block.
type Segment struct {
	BlockIndex       int        `json:"block_index"`
	StartSample      int64      `json:"start_sample"`
	EndSample        int64      `json:"end_sample"`
	InputDigest      string     `json:"input_digest"`
	SegmentHash      string     `json:"segment_hash"`
	Frames           int        `json:"frames"`
	PixelMean        float64    `json:"pixel_mean"`
	PeakDBFS         float64    `json:"peak_dbfs"`
	AlignmentSamples int64      `json:"alignment_error_samples"`
	Gap              bool       `json:"gap"`
	GapSamples       int64      `json:"gap_samples,omitempty"`
	GapRanges        []GapRange `json:"gap_ranges,omitempty"`
	FrameHashes      []string   `json:"frame_hashes"`
}

// GapRange mirrors the transport-layer gap marker for manifest output.
type GapRange struct {
	StartSample int64 `json:"start_sample"`
	EndSample   int64 `json:"end_sample"`
}

// Input is everything the renderer needs for one block.
type Input struct {
	BlockIndex       int
	StartSample      int64
	EndSample        int64
	PCM              []byte
	InputDigest      string
	PeakDBFS         float64
	AlignmentSamples int64
	GapSamples       int64
	GapRanges        []GapRange
	FramesPerBlock   int
	FrameWidth       int
	FrameHeight      int
}

const (
	defaultFramesPerBlock = 4
	defaultFrameWidth     = 16
	defaultFrameHeight    = 16
)

// Render produces a segment without touching any display subsystem.
// Pixel values derive from the input digest so identical content always
// renders identically, while one changed block renders differently.
func Render(in Input) (Segment, error) {
	if in.FramesPerBlock <= 0 {
		in.FramesPerBlock = defaultFramesPerBlock
	}
	if in.FrameWidth <= 0 {
		in.FrameWidth = defaultFrameWidth
	}
	if in.FrameHeight <= 0 {
		in.FrameHeight = defaultFrameHeight
	}
	seed := digestSeed(in.InputDigest)
	frameHashes := make([]string, 0, in.FramesPerBlock)
	var pixelSum int64
	var pixelCount int64
	seg := Segment{
		BlockIndex:       in.BlockIndex,
		StartSample:      in.StartSample,
		EndSample:        in.EndSample,
		InputDigest:      in.InputDigest,
		PeakDBFS:         round2(in.PeakDBFS),
		AlignmentSamples: in.AlignmentSamples,
		Gap:              in.GapSamples > 0,
		GapSamples:       in.GapSamples,
		GapRanges:        in.GapRanges,
		Frames:           in.FramesPerBlock,
	}
	for f := 0; f < in.FramesPerBlock; f++ {
		frame := renderFrame(seed, f, in.FrameWidth, in.FrameHeight)
		frameHashes = append(frameHashes, frame.Hash)
		pixelSum += int64(frame.Mean * float64(len(frame.Pixels)))
		pixelCount += int64(len(frame.Pixels))
	}
	if pixelCount > 0 {
		seg.PixelMean = round2(float64(pixelSum) / float64(pixelCount))
	}
	seg.FrameHashes = frameHashes
	seg.SegmentHash = segmentHash(in.InputDigest, frameHashes, in.StartSample, in.EndSample)
	return seg, nil
}

// renderFrame builds a deterministic grayscale gradient modulated by the
// block seed and frame index.
func renderFrame(seed uint64, frameIndex, width, height int) Frame {
	pixels := make([]byte, width*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			v := (x*7 + y*13 + frameIndex*29 + int(seed%251)) % 256
			pixels[y*width+x] = byte(v)
		}
	}
	h := sha256.New()
	h.Write(pixels)
	var sum int
	for _, p := range pixels {
		sum += int(p)
	}
	return Frame{
		Index: frameIndex, Width: width, Height: height, Pixels: pixels,
		Mean: round2(float64(sum) / float64(len(pixels))),
		Hash: hex.EncodeToString(h.Sum(nil)),
	}
}

// digestSeed folds a hex digest into a uint64 seed.
func digestSeed(digest string) uint64 {
	var seed uint64
	for i := 0; i+1 < len(digest); i += 2 {
		var b byte
		for _, c := range digest[i : i+2] {
			b <<= 4
			switch {
			case c >= '0' && c <= '9':
				b |= byte(c - '0')
			case c >= 'a' && c <= 'f':
				b |= byte(c-'a') + 10
			}
		}
		seed = seed*31 + uint64(b)
	}
	return seed
}

func segmentHash(inputDigest string, frameHashes []string, start, end int64) string {
	h := sha256.New()
	h.Write([]byte("continuity-segment-v1\n"))
	h.Write([]byte(inputDigest))
	h.Write([]byte{'\n'})
	writeInt := func(v int64) {
		var buf [20]byte
		i := len(buf)
		if v == 0 {
			i--
			buf[i] = '0'
		}
		for v > 0 {
			i--
			buf[i] = byte('0' + v%10)
			v /= 10
		}
		h.Write(buf[i:])
		h.Write([]byte{'\n'})
	}
	writeInt(start)
	writeInt(end)
	for _, fh := range frameHashes {
		h.Write([]byte(fh))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// WriteSegment atomically writes a segment JSON artifact under outDir.
func WriteSegment(outDir string, seg Segment) (string, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", media.WrapError(media.CatIO, "render: mkdir", err)
	}
	final := filepath.Join(outDir, segmentFileName(seg.BlockIndex))
	tmp := final + ".tmp"
	raw, err := json.MarshalIndent(seg, "", "  ")
	if err != nil {
		return "", media.WrapError(media.CatIO, "render: marshal", err)
	}
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return "", media.WrapError(media.CatIO, "render: temp write", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return "", media.WrapError(media.CatIO, "render: rename", err)
	}
	return final, nil
}

func segmentFileName(index int) string {
	return "segment-" + padIndex(index) + ".json"
}

func padIndex(i int) string {
	s := ""
	if i == 0 {
		return "000000"
	}
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	for len(s) < 6 {
		s = "0" + s
	}
	return s
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
