// Package fixture renders deterministic sample clips for offline demos.
// Everything is generated from a fixed seed: repeated runs produce byte
// identical video files, complexity samples and fingerprints.
package fixture

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"transcode/internal/media"
)

// Spec describes the sample clip to generate.
type Spec struct {
	Path        string
	SidecarPath string
	DurationSec float64
	FPS         int
	Width       int
	Height      int
}

// complexityAt is the deterministic scene model: quiet start and end with
// an action-heavy, oscillating middle. Values stay in [0.2, 4].
func complexityAt(t float64) float64 {
	switch {
	case t < 0.25:
		return 0.2 + 0.05*math.Sin(t*7)
	case t > 0.75:
		return 0.4 + 0.05*math.Cos(t*9)
	default:
		return 2.1 + 1.9*math.Abs(math.Sin(t*6))
	}
}

// Generate writes the sample video and its complexity-sample sidecar.
// The sidecar also pins one black frame and one duplicated ("identical")
// frame so PSNR edge-case handling is exercised on every demo run.
func Generate(s Spec) (*media.Video, []float64, error) {
	if s.DurationSec <= 0 || s.FPS <= 0 || s.Width <= 0 || s.Height <= 0 {
		return nil, nil, fmt.Errorf("invalid fixture spec: %+v", s)
	}
	nFrames := int(math.Round(s.DurationSec * float64(s.FPS)))
	if nFrames < 4 {
		return nil, nil, fmt.Errorf("duration too short: %d frames", nFrames)
	}
	plane := s.Width * s.Height
	v := &media.Video{Width: s.Width, Height: s.Height, FPS: s.FPS,
		Frames: make([][]byte, nFrames)}
	samples := make([]float64, nFrames)
	blackAt := nFrames / 4
	dupAt := nFrames / 2

	for f := 0; f < nFrames; f++ {
		t := float64(f) / float64(nFrames)
		c := complexityAt(t)
		samples[f] = c
		frame := make([]byte, plane)
		for p := 0; p < plane; p++ {
			// Deterministic PRNG: mixed linear congruence on frame/pixel.
			seed := uint32(f)*1103515245 + uint32(p)*22695477 + 12345
			noise := float64(seed%17) * 0.5 * c
			base := 60 + 120*(0.5+0.5*math.Sin(t*12+float64(p)*0.07))*c/4
			val := base + noise
			switch {
			case val < 0:
				val = 0
			case val > 255:
				val = 255
			}
			frame[p] = byte(math.Round(val))
		}
		v.Frames[f] = frame
	}
	// Pin edge cases after the main pass so their positions are stable.
	for p := range v.Frames[blackAt] {
		v.Frames[blackAt][p] = 0
	}
	copy(v.Frames[dupAt], v.Frames[dupAt-1])

	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return nil, nil, err
	}
	if err := media.WriteVideo(s.Path, v); err != nil {
		return nil, nil, err
	}
	if s.SidecarPath != "" {
		var b strings.Builder
		for i, c := range samples {
			b.WriteString(strconv.FormatFloat(c, 'f', 6, 64))
			if i != len(samples)-1 {
				b.WriteByte('\n')
			}
		}
		if err := os.WriteFile(s.SidecarPath, []byte(b.String()), 0o644); err != nil {
			return nil, nil, err
		}
	}
	return v, samples, nil
}

// LoadSamples reads the one-value-per-line complexity sidecar.
func LoadSamples(path string) ([]float64, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	out := make([]float64, 0, len(lines))
	for i, line := range lines {
		var v float64
		if _, err := fmt.Sscanf(line, "%g", &v); err != nil {
			return nil, fmt.Errorf("sidecar line %d: %w", i+1, err)
		}
		out = append(out, v)
	}
	return out, nil
}
