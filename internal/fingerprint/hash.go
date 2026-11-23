// Package fingerprint computes deterministic content hashes over normalized
// audio windows and sampled video frames, and reports duplicate content even
// when container metadata differs.
package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"

	"mediapipe/internal/model"
)

// Match describes a duplicate-content finding between two segments.
type Match struct {
	LeftIndex  int    `json:"left_index"`
	RightIndex int    `json:"right_index"`
	Kind       string `json:"kind"` // "audio" or "video"
	Threshold  int    `json:"threshold_bits"`
	// Distance is the number of differing hash bytes (0 = identical).
	Distance int `json:"distance_bytes"`
}

// SegmentHash bundles the hashes computed for one cut segment.
type SegmentHash struct {
	SegmentIndex int    `json:"segment_index"`
	AudioHash    string `json:"audio_hash"`
	VideoHash    string `json:"video_hash"`
	VideoFrames  int    `json:"video_frames"`
	AudioWindows int    `json:"audio_windows"`
}

// Hasher holds dedup policy.
type Hasher struct {
	// AudioWindowNanos normalizes audio into fixed windows before hashing.
	AudioWindowNanos int64
	// FrameStride samples every Nth video frame to bound work.
	FrameStride int
	// MaxDistance is the largest byte-wise hash distance still a match.
	MaxDistance int
}

// DefaultHasher returns the demo dedup configuration.
func DefaultHasher() Hasher {
	return Hasher{AudioWindowNanos: 20_000_000, FrameStride: 3, MaxDistance: 0}
}

// HashSegment hashes the samples belonging to one segment's sample range.
func (h Hasher) HashSegment(segIndex int, video, audioTrack *model.Track,
	startSample, endSample int) SegmentHash {
	out := SegmentHash{SegmentIndex: segIndex}
	if video != nil {
		out.VideoHash, out.VideoFrames = h.hashVideo(video, startSample, endSample)
	}
	if audioTrack != nil {
		out.AudioHash, out.AudioWindows = h.hashAudio(audioTrack)
	}
	return out
}

// hashVideo combines the per-frame content keys (SHA-256 of each frame's real
// payload bytes) over sampled frames. Identical frame content yields the same
// combined hash even when offsets or container metadata differ.
func (h Hasher) hashVideo(tr *model.Track, start, end int) (string, int) {
	sum := sha256.New()
	frames := 0
	stride := h.FrameStride
	if stride <= 0 {
		stride = 1
	}
	for i := start; i < end && i < len(tr.Samples); i += stride {
		s := tr.Samples[i]
		if s.ContentKey == "" {
			continue
		}
		// Domain-separate keyframe content so the hash stays unambiguous.
		if s.Keyframe {
			sum.Write([]byte{'k'})
		} else {
			sum.Write([]byte{'p'})
		}
		sum.Write([]byte(s.ContentKey))
		frames++
	}
	if frames == 0 {
		return "", 0
	}
	return hex.EncodeToString(sum.Sum(nil)), frames
}

// hashAudio normalizes PCM into fixed windows and hashes each window's RMS
// quantized to a stable integer, so level-equivalent content collides.
func (h Hasher) hashAudio(tr *model.Track) (string, int) {
	sum := sha256.New()
	win := h.AudioWindowNanos
	if win <= 0 {
		win = 20_000_000
	}
	windows := 0
	var cur []int16
	var curEnd int64 = -1
	flush := func() {
		if len(cur) == 0 {
			return
		}
		var sq float64
		for _, v := range cur {
			x := float64(v)
			sq += x * x
		}
		rms := int64(0)
		if len(cur) > 0 {
			rms = int64(sq / float64(len(cur)))
		}
		var buf [8]byte
		putInt64(buf[:], rms)
		sum.Write(buf[:])
		windows++
		cur = cur[:0]
	}
	for _, s := range tr.Samples {
		base := s.PTS + tr.OffsetNanos
		step := int64(1)
		if len(s.Pcm) > 0 && s.DurationNanos > 0 {
			step = s.DurationNanos / int64(len(s.Pcm))
		}
		for _, v := range s.Pcm {
			t := base
			if curEnd < 0 || t >= curEnd {
				flush()
				curEnd = t + win
			}
			cur = append(cur, v)
			base += step
		}
	}
	flush()
	return hex.EncodeToString(sum.Sum(nil)), windows
}

// FindDups returns all pairwise matches among segment hashes within policy.
func FindDups(hashes []SegmentHash, h Hasher) []Match {
	var out []Match
	for i := 0; i < len(hashes); i++ {
		for j := i + 1; j < len(hashes); j++ {
			if d, ok := byteDistance(hashes[i].VideoHash, hashes[j].VideoHash); ok && d <= h.MaxDistance {
				out = append(out, Match{i, j, "video", 256, d})
			}
			if d, ok := byteDistance(hashes[i].AudioHash, hashes[j].AudioHash); ok && d <= h.MaxDistance {
				out = append(out, Match{i, j, "audio", 256, d})
			}
		}
	}
	return out
}

func byteDistance(a, b string) (int, bool) {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0, false
	}
	d := 0
	for i := 0; i < len(a); i++ {
		if a[i] != b[i] {
			d++
		}
	}
	return d, true
}

func putInt64(b []byte, v int64) {
	for i := 7; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
}
