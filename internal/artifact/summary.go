package artifact

import (
	"crypto/sha256"
	"encoding/hex"

	"medialab/internal/pixel"
)

// Summarize aggregates per-frame records into the terminal summary. It is a
// pure function of the records, so the same artefacts always yield the same
// numbers regardless of scheduling order.
func Summarize(frames []Frame, segments []Segment) Summary {
	sum := Summary{Tracks: len(segments), Frames: len(frames)}
	sum.PeakLoudnessDBFS = pixel.LoudnessFloorDBFS
	var sr, sg, sb, sv float64
	for _, f := range frames {
		if f.Fill {
			sum.FillFrames++
		}
		sum.TotalBytes += f.Bytes
		sr += f.Stats.MeanR
		sg += f.Stats.MeanG
		sb += f.Stats.MeanB
		sv += f.Stats.Variance
		if f.LoudnessDBFS > sum.PeakLoudnessDBFS {
			sum.PeakLoudnessDBFS = f.LoudnessDBFS
		}
	}
	for _, s := range segments {
		sum.TotalBytes += s.Bytes
	}
	if n := float64(len(frames)); n > 0 {
		sum.MeanR, sum.MeanG, sum.MeanB = sr/n, sg/n, sb/n
		sum.Variance = sv / n
	}
	sum.RunHash = RunHash(frames, segments)
	return sum
}

// RunHash is the stable digest of every artefact hash in order. Two runs with
// the same input and parameters must produce the same value.
func RunHash(frames []Frame, segments []Segment) string {
	h := sha256.New()
	h.Write([]byte("medialab-run-v1\n"))
	for _, f := range frames {
		h.Write([]byte(f.SHA256))
		h.Write([]byte{'\n'})
	}
	h.Write([]byte("segments\n"))
	for _, s := range segments {
		h.Write([]byte(s.SHA256))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}
