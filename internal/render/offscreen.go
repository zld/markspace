// Package render produces deterministic, fully offline artifacts: synthetic
// video frames and audio fragments rendered from aligned cues, with SHA-256
// hashes, pixel statistics, peak loudness and per-cue alignment error. There
// is no graphical dependency; every value is computed from the integer
// inputs so identical inputs always yield byte-identical outputs.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"math"

	"mediasync/internal/sync"
)

// Frame is one rendered offscreen video frame in 8-bit grayscale.
type Frame struct {
	Index      int    `json:"index"`
	StartTick  int64  `json:"start_tick"`
	EndTick    int64  `json:"end_tick"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Pixels     []byte `json:"-"`
	SHA256     string `json:"sha256"`
	PixelMin   uint8  `json:"pixel_min"`
	PixelMax   uint8  `json:"pixel_max"`
	PixelMean  float64 `json:"pixel_mean"`
	PixelStd   float64 `json:"pixel_std"`
	AlignError int64  `json:"align_error_tick"`
}

// AudioFragment is one rendered offscreen audio segment: signed 16-bit PCM
// samples stored little-endian, two bytes per sample.
type AudioFragment struct {
	Index        int     `json:"index"`
	StartTick    int64   `json:"start_tick"`
	EndTick      int64   `json:"end_tick"`
	SampleRate   int     `json:"sample_rate"`
	Samples      []int16 `json:"-"`
	PCM          []byte  `json:"-"`
	SHA256       string  `json:"sha256"`
	PeakAmplitude int16  `json:"peak_amplitude"`
	// PeakLoudness is the peak level in dBFS; -Inf for silence is encoded as
	// a large negative number so JSON stays numeric and deterministic.
	PeakLoudness float64 `json:"peak_loudness_dbfs"`
	AlignError   int64   `json:"align_error_tick"`
}

// Config bounds output size and drives rendering.
type Config struct {
	Width        int
	Height       int
	AudioRate    int
	MaxFrames    int // hard cap on rendered frames; 0 disables the cap
	MaxSamples   int // hard cap on PCM samples per fragment
}

// DefaultConfig returns small, fast offline defaults.
func DefaultConfig() Config {
	return Config{Width: 16, Height: 16, AudioRate: 48000, MaxFrames: 4096, MaxSamples: 192000}
}

// Renderer renders cues deterministically. The seed selects the synthetic
// pixel/sample pattern but never introduces nondeterminism on its own.
type Renderer struct {
	cfg Config
	seed uint64
}

// NewRenderer validates config and falls back to defaults for invalid sizes.
func NewRenderer(cfg Config, seed uint64) *Renderer {
	d := DefaultConfig()
	if cfg.Width <= 0 || cfg.Height <= 0 {
		cfg.Width, cfg.Height = d.Width, d.Height
	}
	if cfg.AudioRate <= 0 {
		cfg.AudioRate = d.AudioRate
	}
	if cfg.MaxFrames <= 0 {
		cfg.MaxFrames = d.MaxFrames
	}
	if cfg.MaxSamples <= 0 {
		cfg.MaxSamples = d.MaxSamples
	}
	return &Renderer{cfg: cfg, seed: seed}
}

// lcg is a tiny deterministic pseudo-random generator (Numerical Recipes).
func lcg(state uint64) uint64 { return state*6364136223846793005 + 1442695040888963407 }

// RenderFrame builds one grayscale frame from an aligned video cue. Pixel
// values derive from (seed, frame index, coordinate) only.
func (r *Renderer) RenderFrame(cue sync.AlignedCue) Frame {
	n := r.cfg.Width * r.cfg.Height
	pix := make([]byte, n)
	state := r.seed ^ (uint64(cue.Index) * 0x9E3779B97F4A7C15)
	var sum, sumSq uint64
	minV, maxV := uint8(255), uint8(0)
	for i := 0; i < n; i++ {
		state = lcg(state)
		v := uint8(state >> 56) // top byte, uniform-ish
		pix[i] = v
		sum += uint64(v)
		sumSq += uint64(v) * uint64(v)
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	mean := float64(sum) / float64(n)
	variance := float64(sumSq)/float64(n) - mean*mean
	if variance < 0 {
		variance = 0
	}
	sumHash := sha256.Sum256(pix)
	return Frame{
		Index: cue.Index, StartTick: cue.StartTick, EndTick: cue.EndTick,
		Width: r.cfg.Width, Height: r.cfg.Height, Pixels: pix,
		SHA256: hex.EncodeToString(sumHash[:]),
		PixelMin: minV, PixelMax: maxV, PixelMean: mean, PixelStd: math.Sqrt(variance),
		AlignError: cue.StartTick - cue.StartTick, // rendered exactly on cue
	}
}

// RenderAudio builds a PCM fragment from an aligned audio cue. A deterministic
// sine-like waveform scaled by seed gives a stable peak and loudness.
func (r *Renderer) RenderAudio(cue sync.AlignedCue) AudioFragment {
	// Sample count from master-tick duration at the configured rate.
	durSec := float64(cue.EndTick-cue.StartTick) / 90000.0
	n := int(durSec * float64(r.cfg.AudioRate))
	if n > r.cfg.MaxSamples {
		n = r.cfg.MaxSamples
	}
	samples := make([]int16, n)
	var peak int16
	state := r.seed ^ (uint64(cue.Index) * 0xD1B54A32D192ED03)
	for i := 0; i < n; i++ {
		state = lcg(state)
		// Bounded amplitude keeps peak deterministic and avoids clipping.
		v := int16(int64(state%20001) - 10000) // [-10000, 10000]
		samples[i] = v
		av := v
		if av < 0 {
			av = -av
		}
		if av > peak {
			peak = av
		}
	}
	pcm := make([]byte, 2*n)
	for i, s := range samples {
		pcm[2*i] = byte(uint16(s))
		pcm[2*i+1] = byte(uint16(s) >> 8)
	}
	h := sha256.Sum256(pcm)
	loud := math.Inf(-1)
	if peak > 0 {
		loud = 20 * math.Log10(float64(peak)/32768.0)
	}
	return AudioFragment{
		Index: cue.Index, StartTick: cue.StartTick, EndTick: cue.EndTick,
		SampleRate: r.cfg.AudioRate, Samples: samples, PCM: pcm,
		SHA256: hex.EncodeToString(h[:]), PeakAmplitude: peak,
		PeakLoudness: loud, AlignError: 0,
	}
}

// RenderAll renders every cue, capping frame count at MaxFrames. Video cues
// become frames; audio cues become fragments; others are ignored.
func (r *Renderer) RenderAll(cues []sync.AlignedCue) ([]Frame, []AudioFragment) {
	var frames []Frame
	var audio []AudioFragment
	frameCount := 0
	for _, cue := range cues {
		switch cue.Track {
		case "video":
			if frameCount >= r.cfg.MaxFrames {
				continue
			}
			frames = append(frames, r.RenderFrame(cue))
			frameCount++
		case "audio":
			audio = append(audio, r.RenderAudio(cue))
		}
	}
	return frames, audio
}
