package demo

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"mediad/internal/audio"
	"mediad/internal/batch"
	"mediad/internal/clock"
)

func (p *Program) chunkFrames(c batch.PlanChunk) int {
	return int(clock.Scale{Num: int64(p.Opt.TargetRate)}.FromNanos(c.Span.End - c.Span.Start))
}

// RenderChunk mixes all EDL segments covering this interval with linear
// equal-power? (coefficient-sum-one) crossfades. Source positions are derived
// from absolute nanosecond coordinates, so chunk boundaries do not accumulate
// resampler phase.
func (p *Program) RenderChunk(pc batch.PlanChunk, tmpPath string) error {
	frames := p.chunkFrames(pc)
	out, err := audio.NewBuffer(p.Opt.TargetRate, p.Layout, frames)
	if err != nil {
		return err
	}
	rate := float64(p.Opt.TargetRate)

	for i, seg := range p.EDL.Segments {
		if !seg.Span().Overlaps(pc.Span) {
			continue
		}
		track := p.Tracks[seg.Track]
		if track == nil {
			continue
		}
		region, err := audio.ResampleRegion(track, pc.Span.Start-seg.StartNanos, frames, p.Opt.TargetRate)
		if err != nil {
			return err
		}
		var nextTransition int64
		var fadeOutEnd int64
		if i+1 < len(p.EDL.Segments) {
			next := p.EDL.Segments[i+1]
			nextTransition = next.TransitionInNanos
			fadeOutEnd = seg.End()
		}

		for j := 0; j < frames; j++ {
			globalSec := float64(pc.Span.Start)/1e9 + float64(j)/rate
			localSec := globalSec - float64(seg.StartNanos)/1e9
			gain := 1.0
			switch {
			case localSec < 0:
				gain = 0
			case seg.TransitionInNanos > 0 && localSec < float64(seg.TransitionInNanos)/1e9:
				gain = localSec / (float64(seg.TransitionInNanos) / 1e9)
			}
			if nextTransition > 0 &&
				globalSec >= float64(p.EDL.Segments[i+1].StartNanos)/1e9 &&
				globalSec < float64(fadeOutEnd)/1e9 {
				outGain := (float64(fadeOutEnd)/1e9 - globalSec) /
					(float64(nextTransition) / 1e9)
				if outGain < 0 {
					outGain = 0
				}
				gain *= outGain
			}
			if gain < 0 {
				gain = 0
			} else if gain > 1 {
				gain = 1
			}
			for c := 0; c < p.Layout.Channels; c++ {
				out.Planar[c][j] += gain * region.Planar[c][j]
			}
		}
	}
	return audio.WriteWAV16(tmpPath, out)
}

func (p *Program) verifyChunk(path string, pc batch.PlanChunk) error {
	_, err := audio.VerifyWAV(path, p.Opt.TargetRate, p.Layout.Channels, p.chunkFrames(pc))
	return err
}

// Execute runs the checkpoint-backed chunk pipeline.
func (p *Program) Execute() (*batch.Index, batch.RunReport, error) {
	r := &batch.Runner{
		Dir:         p.Opt.Out,
		Fingerprint: p.Fingerprint,
		Render:      p.RenderChunk,
		Verify:      p.verifyChunk,
	}
	return r.Run(p.Plan)
}

// HashChunkOutputs hashes only committed media chunk contents with their
// relative names. It remains stable across first and reused runs.
func (p *Program) HashChunkOutputs(idx *batch.Index) (string, error) {
	h := sha256.New()
	for _, rec := range idx.Chunks {
		path := filepath.Join(p.Opt.Out, rec.File)
		sum, _, err := batch.HashFile(path)
		if err != nil {
			return "", err
		}
		_, _ = h.Write([]byte(rec.File))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(sum))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileBytes(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}
