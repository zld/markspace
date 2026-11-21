// Package rate allocates the total bitrate budget across segments and
// corrects future targets from measured encoding error. Vector math and
// the error regression use gonum; frames are streamed at low resolution
// so analysis never holds a whole video in memory.
package rate

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"

	"gonum.org/v1/gonum/floats"
	"gonum.org/v1/gonum/stat"

	"transcode/internal/segment"
)

// Metrics describes one segment's coding difficulty, extracted from a
// low-resolution pre-analysis pass.
type Metrics struct {
	Motion  float64 `json:"motion"`  // mean abs luma diff between consecutive frames
	Texture float64 `json:"texture"` // mean abs spatial gradient
}

// weight combines metrics into a relative bitrate demand.
func (m Metrics) weight() float64 {
	return 0.25 + m.Motion + 0.5*m.Texture
}

// Analyze streams the input at low resolution and computes per-segment
// motion and texture metrics. Frame i maps to segment by timestamp.
func Analyze(ctx context.Context, path string, fps float64, segs []segment.Segment) ([]Metrics, error) {
	const w, h = 160, 90
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-v", "error", "-nostdin", "-i", path,
		"-map", "0:v:0", "-vf", fmt.Sprintf("scale=%d:%d", w, h),
		"-f", "rawvideo", "-pix_fmt", "gray", "pipe:1")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("pre-analysis decode: %w", err)
	}
	type acc struct{ motion, texture float64; frames int }
	accs := make([]acc, len(segs))
	frameSize := w * h
	buf := make([]byte, frameSize)
	prev := make([]byte, frameSize)
	frameIdx := 0
	for {
		if _, err := io.ReadFull(pipe, buf); err != nil {
			break
		}
		t := float64(frameIdx) / fps
		si := len(segs) - 1
		for i, s := range segs {
			if t < s.EndSec {
				si = i
				break
			}
		}
		var tex, mot float64
		for y := 0; y < h; y++ {
			row := y * w
			for x := 1; x < w; x++ {
				tex += absDiff(buf[row+x], buf[row+x-1])
				mot += absDiff(buf[row+x], prev[row+x])
			}
		}
		n := float64(h * (w - 1))
		accs[si].texture += tex / n
		if frameIdx > 0 {
			accs[si].motion += mot / n
		}
		accs[si].frames++
		copy(prev, buf)
		frameIdx++
	}
	cmd.Wait()
	if frameIdx == 0 {
		return nil, fmt.Errorf("pre-analysis decoded no frames")
	}
	out := make([]Metrics, len(segs))
	for i, a := range accs {
		if a.frames > 0 {
			out[i] = Metrics{Motion: a.motion / float64(a.frames), Texture: a.texture / float64(a.frames)}
		}
	}
	return out, nil
}

func absDiff(a, b byte) float64 {
	if a > b {
		return float64(a - b)
	}
	return float64(b - a)
}

// Controller hands out per-segment bitrate targets under a total
// budget and applies error feedback from measured actuals. Safe for
// concurrent use by scheduler workers.
type Controller struct {
	mu         sync.Mutex
	budgetBits float64   // remaining bits for unassigned segments
	weights    []float64 // demand weight per segment
	durs       []float64
	assigned   []bool
	minKbps    float64
	maxKbps    float64
	targets    []float64 // history for error regression
	actuals    []float64
	alpha      float64 // fitted actual = alpha*target + beta
	beta       float64
}

// New builds a Controller. totalKbps is the average bitrate goal over
// the whole asset; per-segment results may deviate within [min,max].
func New(totalKbps float64, segs []segment.Segment, m []Metrics, minKbps, maxKbps float64) *Controller {
	c := &Controller{
		weights:  make([]float64, len(segs)),
		durs:     make([]float64, len(segs)),
		assigned: make([]bool, len(segs)),
		minKbps:  minKbps,
		maxKbps:  maxKbps,
		alpha:    1.0,
	}
	var totalDur float64
	for i, s := range segs {
		c.weights[i] = m[i].weight() * s.Duration()
		c.durs[i] = s.Duration()
		totalDur += s.Duration()
	}
	c.budgetBits = totalKbps * 1000 * totalDur
	return c
}

// Target returns the bitrate in kbps for segment i, proportional to
// its share of the remaining demand weight, clamped to [min,max] and
// corrected by the fitted error model.
func (c *Controller) Target(i int) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	remaining := make([]float64, len(c.weights))
	for j := range c.weights {
		if !c.assigned[j] {
			remaining[j] = c.weights[j]
		}
	}
	sum := floats.Sum(remaining)
	if sum <= 0 {
		return c.minKbps
	}
	kbps := c.budgetBits * (c.weights[i] / sum) / c.durs[i] / 1000
	// Error correction: if the encoder systematically overshoots
	// (alpha > 1), aim lower so the actual lands on target.
	if len(c.targets) >= 2 && c.alpha > 0.5 && c.alpha < 2.0 {
		kbps = (kbps - c.beta) / c.alpha
	}
	if kbps < c.minKbps {
		kbps = c.minKbps
	}
	if kbps > c.maxKbps {
		kbps = c.maxKbps
	}
	c.assigned[i] = true
	return kbps
}

// Feedback records the measured bitrate of a finished segment, debits
// the remaining budget by the actual spend, and refits the linear
// error model actual = alpha*target + beta via gonum least squares.
func (c *Controller) Feedback(i int, targetKbps, actualKbps float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.budgetBits -= actualKbps * 1000 * c.durs[i]
	c.targets = append(c.targets, targetKbps)
	c.actuals = append(c.actuals, actualKbps)
	if len(c.targets) >= 2 {
		alpha, beta := stat.LinearRegression(c.targets, c.actuals, nil, false)
		if alpha > 0.2 && alpha < 5 {
			c.alpha, c.beta = alpha, beta
		}
	}
}
