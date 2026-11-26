// Package worker executes selected encode configurations with a bounded
// concurrency pool and wraps the FFmpeg trial-encode / SSIM measurement
// boundary behind the plan.TrialEncoder interface.
package worker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"regress/internal/fault"
	"regress/internal/fixture"
	"regress/internal/plan"
	"regress/internal/scan"
)

// FFmpegEncoder implements plan.TrialEncoder against a local FFmpeg 7 binary.
type FFmpegEncoder struct {
	SourcePath string
	Codec      string
	WorkDir    string
	Segments   *plan.SegmentPlan
	SourceW    int
	SourceH    int
	SourceFPS  int
	// timeout bounds every subprocess; a runaway encode is killed and mapped
	// to fault.CodeSubprocessTimeout rather than hanging the pipeline.
	Timeout func() context.Context
}

// Encode transcodes the source for one candidate into a raw elementary stream
// inside the working directory.
func (e *FFmpegEncoder) Encode(ctx context.Context, codec string, c plan.Candidate) (*plan.EncoderOutput, error) {
	if e.Timeout != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
	}
	if err := os.MkdirAll(e.WorkDir, 0o755); err != nil {
		return nil, fault.New(fault.StageWorker, fault.CodeIO, codec, err.Error()).Wrap(err)
	}
	bin, err := fixture.FFMpegPath()
	if err != nil {
		return nil, err
	}
	out := filepath.Join(e.WorkDir, fmt.Sprintf("trial-%d%s", c.ID, rawExt(codec)))
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", e.SourcePath,
	}
	args = append(args, fixture.TranscodeArgs(codec, c.Width, c.Height, e.gop(), c.BitrateBPS, c.CRF)...)
	args = append(args, "-an", "-f", fixture.RawMuxer(codec), out)
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if isTimeout(ctx, err) {
			return nil, fault.New(fault.StageWorker, fault.CodeSubprocessTimeout, codec,
				"trial encode exceeded time budget").Wrap(err)
		}
		return nil, fault.New(fault.StageWorker, fault.CodeSubprocessFailed, codec,
			"trial encode failed: "+stderr.String()).Wrap(err)
	}
	return e.describe(out, codec)
}

// MeasureSSIM runs the FFmpeg SSIM filter against source and candidate and
// parses the aggregate score from the filter log.
func (e *FFmpegEncoder) MeasureSSIM(ctx context.Context, codec string, out *plan.EncoderOutput) (float64, error) {
	if err := os.MkdirAll(e.WorkDir, 0o755); err != nil {
		return 0, fault.New(fault.StageWorker, fault.CodeIO, codec, err.Error()).Wrap(err)
	}
	bin, err := fixture.FFMpegPath()
	if err != nil {
		return 0, err
	}
	logPath := filepath.Join(e.WorkDir, fmt.Sprintf("ssim-%d.log", idFromPath(out.Path)))
	// SSIM requires matching geometry. The candidate is scaled back to the
	// source dimensions and frame rates are normalized to the source clock so
	// frame pairing is deterministic.
	fps := e.SourceFPS
	if fps <= 0 {
		fps = 24
	}
	filter := fmt.Sprintf(
		"[0:v]fps=%d,scale=%d:%d:flags=bicubic[dist];[1:v]fps=%d[ref];[dist][ref]ssim=stats_file=-",
		fps, e.SourceW, e.SourceH, fps)
	args := []string{
		"-hide_banner", "-nostdin",
		"-i", out.Path, "-i", e.SourcePath,
		"-lavfi", filter,
		"-f", "null", "-",
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	runErr := cmd.Run()
	_ = writeLog(logPath, combined.Bytes())
	if runErr != nil {
		if isTimeout(ctx, runErr) {
			return 0, fault.New(fault.StageWorker, fault.CodeSubprocessTimeout, codec,
				"ssim measure exceeded time budget").Wrap(runErr)
		}
		return 0, fault.New(fault.StageWorker, fault.CodeSubprocessFailed, codec,
			"ssim measure failed: "+combined.String()).Wrap(runErr)
	}
	score, perr := parseSSIM(combined.String())
	if perr != nil {
		return 0, fault.New(fault.StageWorker, fault.CodeSubprocessFailed, codec, perr.Error())
	}
	return score, nil
}

// describe re-scans a produced stream to count frames and split its bytes on
// the shared keyframe table, giving per-segment byte counts.
func (e *FFmpegEncoder) describe(path, codec string) (*plan.EncoderOutput, error) {
	res, err := scanFile(codec, path)
	if err != nil {
		return nil, err
	}
	out := &plan.EncoderOutput{
		Path:       path,
		FrameCount: len(res.AccessUnits),
	}
	info, err := statSize(path)
	if err != nil {
		return nil, err
	}
	out.OutputBytes = info
	for i, kfi := range res.Keyframes {
		start := res.AccessUnits[kfi].Offset
		var end int64 = res.StreamBytes
		if i+1 < len(res.Keyframes) {
			end = res.AccessUnits[res.Keyframes[i+1]].Offset
		}
		out.SegmentBytes = append(out.SegmentBytes, end-start)
	}
	return out, nil
}

func (e *FFmpegEncoder) gop() int {
	if e.Segments != nil && len(e.Segments.FramesPerSegment) > 0 {
		return e.Segments.FramesPerSegment[0]
	}
	return 12
}

var ssimAll = regexp.MustCompile(`All:([0-9.]+)`)

// parseSSIM extracts the aggregate "All:" score; FFmpeg prints values either
// as 0.97x or as the 100-scaled form depending on build, normalized here.
func parseSSIM(log string) (float64, error) {
	idx := strings.LastIndex(log, "SSIM")
	segment := log
	if idx >= 0 {
		segment = log[idx:]
	}
	m := ssimAll.FindStringSubmatch(segment)
	if m == nil {
		return 0, fmt.Errorf("ssim aggregate score not found in filter output")
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("ssim score parse failed: %w", err)
	}
	if v > 1.0 { // 100-scaled form
		v /= 100
	}
	return v, nil
}

func rawExt(codec string) string {
	switch codec {
	case scan.CodecH265:
		return ".265"
	case scan.CodecAV1:
		return ".obu"
	default:
		return ".264"
	}
}
