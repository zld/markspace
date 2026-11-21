// Package media wraps FFmpeg/ffprobe interactions: source probing,
// deterministic fixture generation and frame decoding.
package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"bitrack/internal/model"
)

// ffprobeJSON is the subset of ffprobe's stream/format output we consume.
type ffprobeJSON struct {
	Streams []ffStream `json:"streams"`
	Format  ffFormat   `json:"format"`
}

type ffStream struct {
	CodecType    string `json:"codec_type"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	AvgFrameRate string `json:"avg_frame_rate"`
	RFrameRate   string `json:"r_frame_rate"`
	NbFrames     string `json:"nb_frames"`
	PixFmt       string `json:"pix_fmt"`
	FieldOrder   string `json:"field_order"`
	Duration     string `json:"duration"`
}

type ffFormat struct {
	Duration string `json:"duration"`
}

// parseRate turns an ffprobe rational ("30000/1001" or "25") into a float.
func parseRate(r string) (float64, error) {
	r = strings.TrimSpace(r)
	if r == "" || r == "0/0" {
		return 0, fmt.Errorf("invalid frame rate %q", r)
	}
	parts := strings.SplitN(r, "/", 2)
	num, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, fmt.Errorf("parse frame rate %q: %w", r, err)
	}
	if len(parts) == 1 {
		return num, nil
	}
	den, err := strconv.ParseFloat(parts[1], 64)
	if err != nil || den == 0 {
		return 0, fmt.Errorf("parse frame rate %q: bad denominator", r)
	}
	return num / den, nil
}

// Probe runs ffprobe on path and validates that the source is a local,
// progressive, constant-frame-rate H.264-compatible video. Every rejection
// returns an error whose message names the exact reason.
func Probe(ctx context.Context, path string) (model.ProbeInfo, error) {
	var info model.ProbeInfo
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_format", "-show_streams",
		path)
	out, err := cmd.Output()
	if err != nil {
		return info, fmt.Errorf("ffprobe failed on %s: %w", path, ExplainExit(err))
	}
	var parsed ffprobeJSON
	if err := json.Unmarshal(out, &parsed); err != nil {
		return info, fmt.Errorf("parse ffprobe output: %w", err)
	}

	var vs *ffStream
	for i := range parsed.Streams {
		if parsed.Streams[i].CodecType == "video" {
			vs = &parsed.Streams[i]
			break
		}
	}
	if vs == nil {
		return info, fmt.Errorf("input rejected: no video stream found in %s", path)
	}
	info.HasVideo = true

	if vs.FieldOrder != "" && vs.FieldOrder != "progressive" && vs.FieldOrder != "unknown" {
		return info, fmt.Errorf("input rejected: interlaced video (field_order=%s); only progressive sources are supported", vs.FieldOrder)
	}
	info.FieldOrder = "progressive"

	rfr, err := parseRate(vs.RFrameRate)
	if err != nil {
		return info, fmt.Errorf("input rejected: %w", err)
	}
	afr, err := parseRate(vs.AvgFrameRate)
	if err != nil {
		return info, fmt.Errorf("input rejected: %w", err)
	}
	// A meaningful gap between the base (r_frame_rate) and average rate means
	// timing is variable rather than a constant frame duration.
	if rfr <= 0 || afr <= 0 || absRate(rfr-afr)/rfr > 0.02 {
		return info, fmt.Errorf("input rejected: variable frame rate detected (r=%s avg=%s); CFR sources only",
			vs.RFrameRate, vs.AvgFrameRate)
	}
	info.IsCFR = true
	info.FrameRate = afr

	if vs.Width <= 0 || vs.Height <= 0 {
		return info, fmt.Errorf("input rejected: invalid dimensions %dx%d", vs.Width, vs.Height)
	}
	if vs.Width%2 != 0 || vs.Height%2 != 0 {
		return info, fmt.Errorf("input rejected: incompatible dimensions %dx%d (must be even for yuv420p H.264)", vs.Width, vs.Height)
	}
	info.Width, info.Height = vs.Width, vs.Height
	info.PixelFormat = vs.PixFmt

	durStr := firstNonEmpty(vs.Duration, parsed.Format.Duration)
	if info.DurationSec, err = strconv.ParseFloat(durStr, 64); err != nil || info.DurationSec <= 0 {
		return info, fmt.Errorf("input rejected: unreadable duration %q", durStr)
	}
	if n, perr := strconv.Atoi(vs.NbFrames); perr == nil && n > 0 {
		info.NbFrames = n
	} else {
		info.NbFrames = int(info.DurationSec*afr + 0.5)
	}
	if info.NbFrames <= 0 {
		return info, fmt.Errorf("input rejected: no decodable frames")
	}
	return info, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" && v != "N/A" {
			return v
		}
	}
	return ""
}

func absRate(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// ExplainExit turns an *exec.ExitError into a message that keeps stderr,
// so missing-binary and encoder failures remain locatable.
func ExplainExit(err error) error {
	if ee, ok := err.(*exec.ExitError); ok {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	if strings.Contains(err.Error(), "executable file not found") {
		return fmt.Errorf("%w: required FFmpeg tool is not installed or not on PATH", err)
	}
	return err
}
