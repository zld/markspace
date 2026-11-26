// Package fixture generates all local test material with FFmpeg: raw
// H.264/H.265/AV1 elementary streams with deterministic keyframes plus the
// damaged variants required by the regression scenarios. No network access
// and no pre-existing sample files are used.
package fixture

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"regress/internal/fault"
)

// Spec describes one elementary stream to synthesize.
type Spec struct {
	Codec      string
	Width      int
	Height     int
	FPS        int
	Frames     int
	GOP        int // keyframe interval in frames
}

// FFMpegPath resolves the FFmpeg 7 keg installed by Homebrew first and falls
// back to PATH. The same lookup is shared by the worker package.
func FFMpegPath() (string, error) {
	candidates := []string{
		"/opt/homebrew/opt/ffmpeg@7/bin/ffmpeg", // macOS arm64 Homebrew
		"/usr/local/opt/ffmpeg@7/bin/ffmpeg",    // macOS x86_64 Homebrew
	}
	if env := os.Getenv("FFMPEG_BIN"); env != "" {
		candidates = append([]string{env}, candidates...)
	}
	candidates = append(candidates, "ffmpeg")
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", fault.New(fault.StageWorker, fault.CodeDependencyMissing, "",
		"ffmpeg binary not found; install ffmpeg@7 or set FFMPEG_BIN")
}

// FFProbePath resolves the matching ffprobe binary.
func FFProbePath() (string, error) {
	candidates := []string{
		"/opt/homebrew/opt/ffmpeg@7/bin/ffprobe",
		"/usr/local/opt/ffmpeg@7/bin/ffprobe",
	}
	if env := os.Getenv("FFPROBE_BIN"); env != "" {
		candidates = append([]string{env}, candidates...)
	}
	candidates = append(candidates, "ffprobe")
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", fault.New(fault.StageWorker, fault.CodeDependencyMissing, "",
		"ffprobe binary not found; install ffmpeg@7 or set FFPROBE_BIN")
}

func extFor(codec string) (string, error) {
	switch codec {
	case "h264":
		return ".264", nil
	case "h265":
		return ".265", nil
	case "av1":
		return ".obu", nil
	default:
		return "", fault.New(fault.StageWorker, fault.CodeParse, codec, "unknown codec")
	}
}

// GenerateElementary renders an elementary stream into dir and returns its
// path. Encoding parameters force a closed-GOP, no-B-frame layout so that
// keyframe positions are deterministic across machines and encoders.
func GenerateElementary(ctx context.Context, dir string, spec Spec) (string, error) {
	bin, err := FFMpegPath()
	if err != nil {
		return "", err
	}
	ext, err := extFor(spec.Codec)
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, spec.Codec+ext)
	duration := float64(spec.Frames) / float64(spec.FPS)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "lavfi",
		"-i", fmt.Sprintf("testsrc=size=%dx%d:rate=%d:duration=%.6f",
			spec.Width, spec.Height, spec.FPS, duration),
	}
	args = append(args, encodeArgs(spec)...)
	args = append(args, "-an", "-f", rawMuxer(spec.Codec), out)

	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fault.New(fault.StageWorker, fault.CodeSubprocessFailed, spec.Codec,
			"ffmpeg encode failed: "+stderr.String()).Wrap(err)
	}
	if info, err := os.Stat(out); err != nil || info.Size() == 0 {
		return "", fault.New(fault.StageWorker, fault.CodeSubprocessFailed, spec.Codec,
			"ffmpeg produced no output")
	}
	return out, nil
}

func rawMuxer(codec string) string {
	switch codec {
	case "h265":
		return "hevc"
	case "av1":
		return "obu"
	default:
		return codec
	}
}

func encodeArgs(spec Spec) []string {
	gop := fmt.Sprintf("g=%d", spec.GOP)
	switch spec.Codec {
	case "h264":
		return []string{
			"-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p",
			"-g", fmt.Sprint(spec.GOP), "-bf", "0", "-sc_threshold", "0",
			"-x264-params", gop + ":keyint_min=" + fmt.Sprint(spec.GOP) + ":aud=1:force-cfr=1",
		}
	case "h265":
		// repeat-headers keeps VPS/SPS/PPS at every IDR so downstream raw
		// re-encodes always have the parameter sets they need.
		return []string{
			"-c:v", "libx265", "-preset", "veryfast", "-pix_fmt", "yuv420p",
			"-x265-params", fmt.Sprintf(
				"keyint=%d:min-keyint=%d:bframes=0:aud=1:scenecut=0:repeat-headers=1",
				spec.GOP, spec.GOP),
		}
	default: // av1
		// SVT-AV1 needs an explicit keyint inside svtav1-params; the global
		// -g alone is not honored, so without it keyframes land on the default
		// 7-frame mini-GOP and break the shared segment boundaries.
		return []string{
			"-c:v", "libsvtav1", "-preset", "8", "-pix_fmt", "yuv420p",
			"-g", fmt.Sprint(spec.GOP), "-crf", "28",
			"-svtav1-params", fmt.Sprintf(
				"pred-struct=2:rc=0:enable-tf=0:keyint=%d", spec.GOP),
		}
	}
}

// RunFFmpeg is the shared process wrapper used by candidate trial encodes and
// full transcodes. A nil context is replaced by context.Background.
func RunFFmpeg(ctx context.Context, codec string, args ...string) ([]byte, error) {
	bin, err := FFMpegPath()
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fault.New(fault.StageWorker, fault.CodeSubprocessTimeout, codec,
				"ffmpeg exceeded time budget").Wrap(ctx.Err())
		}
		return nil, fault.New(fault.StageWorker, fault.CodeSubprocessFailed, codec,
			"ffmpeg failed: "+stderr.String()).Wrap(err)
	}
	return stderr.Bytes(), nil
}
