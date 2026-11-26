// Package audio runs the FFmpeg pipeline that decodes local audio tracks,
// resamples them to the delivery format, normalizes loudness and encodes AAC
// candidates at several bitrates. The quality gate is always evaluated from
// measured output, never assumed from encoder settings.
package audio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// Delivery parameters mandated by the specification.
const (
	TargetRateHz     = 48000
	TargetChannels   = 2
	TargetLoudness   = -16.0 // LUFS integrated
	LoudnessTol      = 1.0   // ±LUFS acceptance band
	MaxTruePeakDBTP  = -1.0
	// Normalization aims slightly inside the peak ceiling so measurement
	// rounding cannot push a candidate over the -1 dBTP gate.
	NormPeakTarget   = -1.5
)

// Exit-class sentinels used by the engine to map process failures.
var (
	ErrFFmpegMissing = errors.New("ffmpeg executable not found")
	ErrDecodeFailed  = errors.New("audio decode failed")
)

// Runner abstracts command execution so tests can stub FFmpeg.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error)
}

// ExecRunner invokes real binaries on PATH.
type ExecRunner struct{}

// Run executes name with args, returning captured stdout and stderr.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	if _, err := exec.LookPath(name); err != nil {
		return nil, nil, fmt.Errorf("%w: %s", ErrFFmpegMissing, name)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return out.Bytes(), errBuf.Bytes(), err
}

// DecodeNormalize renders an input track to a 48 kHz stereo signed-16 PCM
// temp file and applies the first loudnorm analysis pass. It returns the
// measured source stats needed for the precise second pass.
func DecodeNormalize(ctx context.Context, r Runner, input, trackSpec, pcmPath string) (LoudnessStats, error) {
	args := []string{
		"-hide_banner", "-nostdin", "-y",
		"-i", input,
		"-map", trackSpec,
		"-vn",
		"-af", fmt.Sprintf("aresample=%d,aformat=channel_layouts=stereo,loudnorm=I=%.1f:TP=%.1f:print_format=json",
			TargetRateHz, TargetLoudness, NormPeakTarget),
		"-f", "s16le", "-acodec", "pcm_s16le",
		"-ar", fmt.Sprint(TargetRateHz), "-ac", fmt.Sprint(TargetChannels),
		pcmPath,
	}
	_, stderr, err := r.Run(ctx, "ffmpeg", args...)
	if err != nil {
		return LoudnessStats{}, fmt.Errorf("%w: %s", ErrDecodeFailed, lastLines(stderr, 4))
	}
	return ParseLoudnessJSON(stderr)
}

// EncodeAAC runs the second, deterministic loudnorm pass and encodes one AAC
// candidate at bitrateKbps. measured are the stats from the analysis pass.
func EncodeAAC(ctx context.Context, r Runner, pcmPath, outPath string, bitrateKbps int, measured LoudnessStats) error {
	args := []string{
		"-hide_banner", "-nostdin", "-y",
		"-f", "s16le", "-ar", fmt.Sprint(TargetRateHz),
		"-ac", fmt.Sprint(TargetChannels), "-i", pcmPath,
		// loudnorm first, then an explicit resample/format so the delivered
		// stream is always exactly 48 kHz stereo regardless of any internal
		// rate change the normalization filter introduces.
		"-af", fmt.Sprintf(
			"loudnorm=I=%.1f:TP=%.1f:linear=true:measured_I=%.2f:measured_TP=%.2f:measured_LRA=%.2f:measured_thresh=%.2f:print_format=summary,aresample=%d,aformat=sample_rates=%d:channel_layouts=stereo",
			TargetLoudness, NormPeakTarget, measured.Integrated, measured.TruePeak,
			measured.Range, measured.Threshold, TargetRateHz, TargetRateHz),
		"-ar", fmt.Sprint(TargetRateHz),
		"-ac", fmt.Sprint(TargetChannels),
		"-c:a", "aac", "-b:a", fmt.Sprintf("%dk", bitrateKbps),
		"-movflags", "+faststart",
		outPath,
	}
	_, stderr, err := r.Run(ctx, "ffmpeg", args...)
	if err != nil {
		return fmt.Errorf("aac encode @%dk: %w: %s", bitrateKbps, err, lastLines(stderr, 4))
	}
	return nil
}

// Measure runs the ebur128 scanner on a finished candidate and parses the
// integrated loudness and true peak from the summary block.
func Measure(ctx context.Context, r Runner, path string) (LoudnessStats, error) {
	args := []string{
		"-hide_banner", "-nostdin",
		"-i", path,
		"-af", "ebur128=peak=true",
		"-f", "null", "-",
	}
	_, stderr, err := r.Run(ctx, "ffmpeg", args...)
	if err != nil {
		return LoudnessStats{}, fmt.Errorf("ebur128 measure: %w: %s", err, lastLines(stderr, 4))
	}
	return ParseEBUR128(stderr)
}

func lastLines(b []byte, n int) string {
	lines := bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n"))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return string(bytes.Join(lines, []byte("\n")))
}
