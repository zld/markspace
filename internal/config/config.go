// Package config validates CLI parameters and runtime configuration for the
// offline transcoding batch processor.
package config

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Limits accepted by Validate.
const (
	MinTargetKbps = 100
	MaxTargetKbps = 100000
	MinSegmentSec = 0.5
	MaxSegmentSec = 3600.0
	MaxWorkers    = 16
	MinSegKbps    = 50
)

// Config holds every knob a run needs. It is serialized into the state file
// so a resume can refuse to mix results produced under different settings.
type Config struct {
	Input       string  `json:"input"`
	TargetKbps  int     `json:"target_kbps"`
	SegmentSec  float64 `json:"segment_sec"`
	Workers     int     `json:"workers"`
	OutDir      string  `json:"out_dir"`
	ReportPath  string  `json:"report_path"`
	StatePath   string  `json:"state_path"`
	Codec       string  `json:"codec"`
	MinSegKbps  int     `json:"min_seg_kbps"`
	MaxSegKbps  int     `json:"max_seg_kbps"`
	MaxRetries  int     `json:"max_retries"`
	PSNRFloorDB float64 `json:"psnr_floor_db"`
}

// Defaults returns a Config with sane defaults; callers override from flags.
func Defaults() Config {
	return Config{
		TargetKbps:  1800,
		SegmentSec:  3.0,
		Workers:     2,
		Codec:       "libx264",
		MinSegKbps:  200,
		MaxSegKbps:  20000,
		MaxRetries:  2,
		PSNRFloorDB: 25.0,
	}
}

// Validate checks every field and the local environment (ffmpeg presence,
// codec availability, directory writability). It returns the first problem
// found; the CLI maps any error here to exit code 2.
func (c *Config) Validate() error {
	if c.TargetKbps < MinTargetKbps || c.TargetKbps > MaxTargetKbps {
		return fmt.Errorf("target-kbps %d out of range [%d,%d]", c.TargetKbps, MinTargetKbps, MaxTargetKbps)
	}
	if c.SegmentSec < MinSegmentSec || c.SegmentSec > MaxSegmentSec {
		return fmt.Errorf("segment duration %.2fs out of range [%.1f,%.1f]", c.SegmentSec, MinSegmentSec, MaxSegmentSec)
	}
	if c.Workers < 1 || c.Workers > MaxWorkers {
		return fmt.Errorf("workers %d out of range [1,%d]", c.Workers, MaxWorkers)
	}
	if c.MinSegKbps < MinSegKbps {
		return fmt.Errorf("min segment bitrate %d kbps below floor %d", c.MinSegKbps, MinSegKbps)
	}
	if c.MaxSegKbps <= c.MinSegKbps {
		return fmt.Errorf("max segment bitrate %d must exceed min %d", c.MaxSegKbps, c.MinSegKbps)
	}
	if c.TargetKbps > c.MaxSegKbps {
		return fmt.Errorf("target bitrate %d exceeds per-segment cap %d", c.TargetKbps, c.MaxSegKbps)
	}
	if c.MaxRetries < 0 || c.MaxRetries > 10 {
		return fmt.Errorf("max-retries %d out of range [0,10]", c.MaxRetries)
	}
	if c.PSNRFloorDB < 0 || c.PSNRFloorDB > 60 {
		return fmt.Errorf("psnr floor %.1f out of range [0,60]", c.PSNRFloorDB)
	}
	if c.OutDir == "" {
		return errors.New("output directory is required")
	}
	abs, err := filepath.Abs(c.OutDir)
	if err != nil {
		return fmt.Errorf("resolve output dir: %w", err)
	}
	c.OutDir = abs
	if err := checkWritableDir(c.OutDir); err != nil {
		return err
	}
	if c.Input != "" {
		in, err := filepath.Abs(c.Input)
		if err != nil {
			return fmt.Errorf("resolve input: %w", err)
		}
		st, err := os.Stat(in)
		if err != nil {
			return fmt.Errorf("input not readable: %w", err)
		}
		if st.IsDir() {
			return fmt.Errorf("input %s is a directory", in)
		}
		c.Input = in
	}
	if c.StatePath == "" {
		c.StatePath = filepath.Join(c.OutDir, "state.json")
	}
	if c.ReportPath == "" {
		c.ReportPath = filepath.Join(c.OutDir, "report.json")
	}
	return checkToolchain(c.Codec)
}

// checkWritableDir creates the directory if needed and probes writability
// with a temporary file that is removed immediately.
func checkWritableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	f, err := os.CreateTemp(dir, ".writetest-*")
	if err != nil {
		return fmt.Errorf("output dir %s not writable: %w", dir, err)
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return nil
}

// checkToolchain verifies ffmpeg/ffprobe exist and the encoder is compiled
// in. All invocations use argv slices, never a shell.
func checkToolchain(codec string) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return errors.New("ffmpeg not found in PATH (brew install ffmpeg)")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return errors.New("ffprobe not found in PATH (brew install ffmpeg)")
	}
	out, err := exec.Command("ffmpeg", "-hide_banner", "-encoders").Output()
	if err != nil {
		return fmt.Errorf("query ffmpeg encoders: %w", err)
	}
	if !strings.Contains(string(out), codec) {
		return fmt.Errorf("encoder %q not available in this ffmpeg build", codec)
	}
	return nil
}
