package synthetic

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
)

// FixtureConfig parameterizes a fully generated offline dataset.
type FixtureConfig struct {
	Seed          int64
	ProgramKey    string
	Segments      int // video segments, each exactly 1s
	Width, Height uint16
	// Corrupt mode: "" | "truncated"
	Corrupt       string
	// CorruptIndex is the segment index that gets damaged.
	CorruptIndex  int
	// LateEvents are event indices delayed past the watermark.
	LateEvents    []int
	DriftRatePPM  float64
	DriftOffsetNS int64
}

// FixturePaths lists every generated file relative to the output root.
type FixturePaths struct {
	Root         string
	HLSManifest  string
	DASHManifest string
	VideoDirs    []string // v1, v2
	AudioDir     string
	RTPFile      string
	EventFile    string
}

// Generate writes the complete dataset under root and returns its layout.
// All randomness derives from cfg.Seed, so reruns are byte-identical.
func Generate(root string, cfg FixtureConfig) (*FixturePaths, error) {
	if cfg.ProgramKey == "" {
		cfg.ProgramKey = "demo"
	}
	if cfg.Segments == 0 {
		cfg.Segments = 10
	}
	if cfg.Width == 0 {
		cfg.Width, cfg.Height = 16, 9
	}
	rng := rand.New(rand.NewSource(cfg.Seed))
	paths := &FixturePaths{Root: root}

	v1Dir := filepath.Join(root, "hls", cfg.ProgramKey, "v1")
	v2Dir := filepath.Join(root, "hls", cfg.ProgramKey, "v2")
	aDir := filepath.Join(root, "hls", cfg.ProgramKey, "audio")
	for _, d := range []string{v1Dir, v2Dir, aDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	paths.VideoDirs = []string{v1Dir, v2Dir}
	paths.AudioDir = aDir

	if err := writeSegments(cfg, v1Dir, v2Dir, aDir); err != nil {
		return nil, err
	}
	if err := writeManifests(cfg, root, paths); err != nil {
		return nil, err
	}
	if err := writeRTP(rng, root, cfg, paths); err != nil {
		return nil, err
	}
	if err := writeEvents(root, cfg, paths); err != nil {
		return nil, err
	}
	return paths, nil
}

// writeSegments emits one video fragment per second for two asset versions
// plus one audio track. v2 differs from v1 only at the conflict index.
func writeSegments(cfg FixtureConfig, v1Dir, v2Dir, aDir string) error {
	const vTS, aTS = uint64(90000), uint64(48000)
	vInit := BuildInitSegment(1, vTS, "video", cfg.Width, cfg.Height)
	if err := os.WriteFile(filepath.Join(v1Dir, "init.mp4"), vInit, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(v2Dir, "init.mp4"), vInit, 0o644); err != nil {
		return err
	}
	aInit := BuildInitSegment(2, aTS, "audio", 0, 0)
	if err := os.WriteFile(filepath.Join(aDir, "init.mp4"), aInit, 0o644); err != nil {
		return err
	}
	for i := 0; i < cfg.Segments; i++ {
		base := uint64(i) * vTS
		samples := vfrSamples(i, cfg.Width, cfg.Height)
		v1 := BuildVideoFragment(1, uint32(i+1), base, samples)
		v2 := BuildVideoFragment(1, uint32(i+1), base, samples)
		if i == conflictIndex(cfg) {
			// Second version carries different pixel content: same interval,
			// different digest -> an explicit content conflict.
			changed := vfrSamples(i+1000, cfg.Width, cfg.Height)
			v2 = BuildVideoFragment(1, uint32(i+1), base, changed)
		}
		if cfg.Corrupt == "truncated" && i == cfg.CorruptIndex {
			cut := len(v1) / 2
			v1 = v1[:cut] // cut mid-box: parser must report the byte offset
		}
		name := fmt.Sprintf("seg%d.m4s", i)
		if err := os.WriteFile(filepath.Join(v1Dir, name), v1, 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(v2Dir, name), v2, 0o644); err != nil {
			return err
		}
		// Audio: one 1s fragment (48k PCM frames) per segment.
		aud := AudioFragment(2, uint32(i+1), uint64(i)*aTS, aTS, 48000, byte(i))
		if err := os.WriteFile(filepath.Join(aDir, name), aud, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// vfrSamples produces three frames per second with alternating VFR durations
// summing exactly to 90000 units, including a non-zero CTS on the middle frame.
func vfrSamples(seg int, w, h uint16) []VideoSample {
	durs := [][2]uint32{{30000, 0}, {30000, 1500}, {30000, 0}}
	out := make([]VideoSample, 3)
	for k, d := range durs {
		out[k] = VideoSample{
			Duration: d[0], CTS: int32(d[1]), Width: w, Height: h,
			Seed: byte(seg*3 + k + 1),
		}
	}
	return out
}

func conflictIndex(cfg FixtureConfig) int {
	// Stable, seed-independent conflict near the middle of the program.
	idx := cfg.Segments / 2
	if cfg.Corrupt == "truncated" && idx == cfg.CorruptIndex {
		idx++
	}
	if idx >= cfg.Segments {
		idx = cfg.Segments - 1
	}
	return idx
}
