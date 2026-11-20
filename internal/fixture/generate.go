// Package fixture renders deterministic synthetic H.264 transport streams
// with a local FFmpeg binary. FFmpeg is used only as a content generator;
// all slicing and playlist logic lives in Go.
package fixture

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

// ErrFFmpegMissing reports that no supported FFmpeg binary is on PATH.
var ErrFFmpegMissing = errors.New("ffmpeg not found on PATH; install with 'brew install ffmpeg' (requires 7.1+)")

// Spec describes one synthetic rendition.
type Spec struct {
	// Bps is the target video bitrate in bits per second.
	Bps int
	// Duration of the generated stream.
	Duration time.Duration
	// FrameRate in frames per second (fixture default is 25).
	FrameRate int
	// GOP is the keyframe interval in frames; 2s at 25 fps => 50.
	GOP int
	// OutPath is the destination .ts file (must already be path-checked).
	OutPath string
}

// minFFmpegMajor is the first supported major release.
const minFFmpegMajor = 7

// VerifyFFmpeg checks presence and minimum version, returning a descriptive
// error instead of a generic exec failure when unavailable.
func VerifyFFmpeg() error {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return ErrFFmpegMissing
	}
	out, err := exec.Command(path, "-version").Output()
	if err != nil {
		return fmt.Errorf("ffmpeg version probe failed: %w", err)
	}
	major, ok := parseMajorVersion(string(out))
	if !ok {
		return fmt.Errorf("cannot parse ffmpeg version from: %q", firstLine(out))
	}
	if major < minFFmpegMajor {
		return fmt.Errorf("ffmpeg %d.x is below the required %d.1", major, minFFmpegMajor)
	}
	return nil
}

// parseMajorVersion extracts the leading major number from the first banner
// line, e.g. "ffmpeg version 7.1 Copyright ..." -> 7.
func parseMajorVersion(banner string) (int, bool) {
	const marker = "ffmpeg version "
	i := indexOf(banner, marker)
	if i < 0 {
		return 0, false
	}
	rest := banner[i+len(marker):]
	digits := takeDigits(rest)
	if digits == "" {
		return 0, false
	}
	v, err := strconv.Atoi(digits)
	return v, err == nil
}

// Generate runs FFmpeg to synthesize a testsrc2 H.264 stream in MPEG-TS.
// The inputs are fully parameterized, so identical specs reproduce identical
// boundary timestamps across runs.
func Generate(ctx context.Context, spec Spec) error {
	if err := VerifyFFmpeg(); err != nil {
		return err
	}
	seconds := spec.Duration.Seconds()
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi",
		"-i", fmt.Sprintf("testsrc2=size=640x360:rate=%d:duration=%.3f", spec.FrameRate, seconds),
		"-f", "lavfi",
		"-i", fmt.Sprintf("sine=frequency=440:sample_rate=48000:duration=%.3f", seconds),
		"-c:v", "libx264",
		"-b:v", strconv.Itoa(spec.Bps),
		"-preset", "veryfast",
		"-pix_fmt", "yuv420p",
		"-g", strconv.Itoa(spec.GOP),
		"-keyint_min", strconv.Itoa(spec.GOP),
		"-sc_threshold", "0",
		"-profile:v", "baseline",
		"-c:a", "aac", "-b:a", "64k",
		"-mpegts_flags", "+pat_pmt_at_frames",
		"-muxrate", "0",
		"-f", "mpegts",
		spec.OutPath,
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg generate %s: %w\n%s", spec.OutPath, err, out)
	}
	return nil
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func takeDigits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

func firstLine(b []byte) string {
	for i, c := range b {
		if c == '\n' {
			return string(b[:i])
		}
	}
	return string(b)
}
