// Package media wraps ffmpeg/ffprobe subprocesses. All arguments are passed
// as separate argv entries; no shell is ever involved.
package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runner executes ffmpeg/ffprobe with a per-invocation timeout.
type Runner struct {
	Timeout time.Duration
}

// run executes bin with args, enforcing the timeout and capturing output.
// On timeout or cancellation the child process is killed and reaped.
func (r Runner) run(ctx context.Context, bin string, args ...string) (string, string, error) {
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return stdout.String(), stderr.String(),
			fmt.Errorf("%s killed after exceeding timeout %s", bin, r.Timeout)
	case errors.Is(ctx.Err(), context.Canceled):
		return stdout.String(), stderr.String(),
			fmt.Errorf("%s terminated: %w", bin, ctx.Err())
	case err != nil:
		exit := "unknown"
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exit = strconv.Itoa(ee.ExitCode())
		}
		return stdout.String(), stderr.String(),
			fmt.Errorf("%s exit status %s: %s", bin, exit, tail(stderr.String(), 300))
	}
	return stdout.String(), stderr.String(), nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "..." + s[len(s)-n:]
	}
	return s
}

// Version returns the first line of `ffmpeg -version`.
func (r Runner) Version(ctx context.Context) (string, error) {
	out, _, err := r.run(ctx, "ffmpeg", "-hide_banner", "-version")
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(out, "\n")
	return strings.TrimSpace(line), nil
}

// ProbeInfo summarizes the primary video stream of an input.
type ProbeInfo struct {
	CodecName string  `json:"codec_name"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	FPS       float64 `json:"fps"`
	Frames    int     `json:"frames"`
	Duration  float64 `json:"duration_sec"`
	SizeBytes int64   `json:"size_bytes"`
}

// Probe reads stream and container metadata via ffprobe.
func (r Runner) Probe(ctx context.Context, path string) (ProbeInfo, error) {
	out, _, err := r.run(ctx, "ffprobe",
		"-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name,width,height,r_frame_rate,nb_frames:format=duration,size",
		"-of", "json", path)
	if err != nil {
		return ProbeInfo{}, fmt.Errorf("probe %s: %w", path, err)
	}
	var payload struct {
		Streams []struct {
			CodecName  string `json:"codec_name"`
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			RFrameRate string `json:"r_frame_rate"`
			NbFrames   string `json:"nb_frames"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
			Size     string `json:"size"`
		} `json:"format"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return ProbeInfo{}, fmt.Errorf("probe %s: parse: %w", path, err)
	}
	if len(payload.Streams) == 0 {
		return ProbeInfo{}, fmt.Errorf("probe %s: no video stream", path)
	}
	s := payload.Streams[0]
	info := ProbeInfo{CodecName: s.CodecName, Width: s.Width, Height: s.Height}
	if info.FPS, err = parseRate(s.RFrameRate); err != nil {
		return ProbeInfo{}, fmt.Errorf("probe %s: frame rate: %w", path, err)
	}
	if n, err := strconv.Atoi(s.NbFrames); err == nil {
		info.Frames = n
	}
	if info.Duration, err = strconv.ParseFloat(payload.Format.Duration, 64); err != nil {
		return ProbeInfo{}, fmt.Errorf("probe %s: duration: %w", path, err)
	}
	if info.SizeBytes, err = strconv.ParseInt(payload.Format.Size, 10, 64); err != nil {
		return ProbeInfo{}, fmt.Errorf("probe %s: size: %w", path, err)
	}
	return info, nil
}

func parseRate(s string) (float64, error) {
	num, den, ok := strings.Cut(s, "/")
	if !ok {
		return strconv.ParseFloat(s, 64)
	}
	n, err1 := strconv.ParseFloat(num, 64)
	d, err2 := strconv.ParseFloat(den, 64)
	if err1 != nil || err2 != nil || d == 0 {
		return 0, fmt.Errorf("invalid rate %q", s)
	}
	return n / d, nil
}

// Timestamps returns the presentation timestamps of every video frame and
// fails explicitly if any timestamp is missing or not strictly increasing.
func (r Runner) Timestamps(ctx context.Context, path string) ([]float64, error) {
	out, _, err := r.run(ctx, "ffprobe",
		"-v", "error", "-select_streams", "v:0",
		"-show_entries", "frame=best_effort_timestamp_time",
		"-of", "csv=p=0", path)
	if err != nil {
		return nil, fmt.Errorf("timestamps %s: %w", path, err)
	}
	var ts []float64
	prev := -1.0
	for i, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimRight(strings.TrimSpace(line), ",")
		if line == "" {
			return nil, fmt.Errorf("timestamps %s: frame %d has no PTS", path, i)
		}
		v, err := strconv.ParseFloat(line, 64)
		if err != nil {
			return nil, fmt.Errorf("timestamps %s: frame %d unparsable PTS %q", path, i, line)
		}
		if i > 0 && v <= prev {
			return nil, fmt.Errorf("timestamps %s: non-monotonic PTS at frame %d (%.6f after %.6f)", path, i, v, prev)
		}
		prev = v
		ts = append(ts, v)
	}
	if len(ts) == 0 {
		return nil, fmt.Errorf("timestamps %s: no frames", path)
	}
	return ts, nil
}

// EncodeParams carries the per-segment encoding settings.
type EncodeParams struct {
	TargetKbps  int
	MaxrateKbps int
	FPS         int
	Preset      string
	PixFmt      string
}

// TranscodeSegment encodes [start, start+dur) of in to out (H.264/MP4) with
// average and peak bitrate constraints. Returns the exact argv used.
func (r Runner) TranscodeSegment(ctx context.Context, in, out string, start, dur float64, p EncodeParams) ([]string, error) {
	args := []string{
		"-hide_banner", "-nostdin", "-y",
		"-ss", formatSec(start), "-t", formatSec(dur), "-i", in,
		"-an", "-c:v", "libx264", "-preset", p.Preset,
		"-b:v", fmt.Sprintf("%dk", p.TargetKbps),
		"-maxrate", fmt.Sprintf("%dk", p.MaxrateKbps),
		"-bufsize", fmt.Sprintf("%dk", p.MaxrateKbps),
		"-r", strconv.Itoa(p.FPS),
		"-pix_fmt", p.PixFmt,
		out,
	}
	_, _, err := r.run(ctx, "ffmpeg", args...)
	return args, err
}

// PSNR compares dist against the [refStart, refStart+refDur) window of ref,
// writing per-frame PSNR to statsFile. Returns the exact argv used.
func (r Runner) PSNR(ctx context.Context, distPath, refPath string, refStart, refDur float64, statsFile string) ([]string, error) {
	args := []string{
		"-hide_banner", "-nostdin",
		"-i", distPath,
		"-ss", formatSec(refStart), "-t", formatSec(refDur), "-i", refPath,
		"-lavfi", fmt.Sprintf("[0:v][1:v]psnr=stats_file=%s", statsFile),
		"-f", "null", "-",
	}
	_, _, err := r.run(ctx, "ffmpeg", args...)
	return args, err
}

// ContainerOverhead returns the container size and the video elementary
// stream size in bytes, so the caller can report muxing overhead separately.
func (r Runner) ContainerOverhead(ctx context.Context, path string) (formatSize, videoBytes int64, err error) {
	out, _, err := r.run(ctx, "ffprobe",
		"-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=bit_rate:format=size,duration",
		"-of", "json", path)
	if err != nil {
		return 0, 0, fmt.Errorf("overhead %s: %w", path, err)
	}
	var payload struct {
		Streams []struct {
			BitRate string `json:"bit_rate"`
		} `json:"streams"`
		Format struct {
			Size     string `json:"size"`
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return 0, 0, fmt.Errorf("overhead %s: parse: %w", path, err)
	}
	formatSize, err = strconv.ParseInt(payload.Format.Size, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("overhead %s: size: %w", path, err)
	}
	if len(payload.Streams) == 0 {
		return 0, 0, fmt.Errorf("overhead %s: no video stream", path)
	}
	br, berr := strconv.ParseFloat(payload.Streams[0].BitRate, 64)
	dur, derr := strconv.ParseFloat(payload.Format.Duration, 64)
	if berr != nil || derr != nil || br <= 0 || dur <= 0 {
		// Stream bitrate unavailable: report zero overhead rather than guessing.
		return formatSize, formatSize, nil
	}
	videoBytes = int64(br * dur / 8)
	return formatSize, videoBytes, nil
}

func formatSec(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }
