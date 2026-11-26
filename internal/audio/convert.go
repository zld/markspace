// Package audio drives the local FFmpeg binary to decode, resample, downmix,
// normalize and encode audio on the shared integer timeline. Every external
// invocation uses fixed argument lists built by this package; payload bytes
// are never passed through a shell or interpreted as commands.
package audio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"

	"rtpregress/internal/clock"
)

// Target spec for every normalized segment.
const (
	TargetRate     = 48000
	TargetChannels = 1
	TargetLUFS     = -16.0
	TargetTP       = -1.0
	TargetBitrate  = 96000

	// pcmFrame is one 16-bit little-endian mono sample at 48 kHz.
	pcmBytesPerSample = 2
)

// PCMBytesPerSample exposes the mono S16LE frame size for duration math.
func PCMBytesPerSample() int { return pcmBytesPerSample }

// Segment is one independently processable chunk of elementary audio bytes.
type Segment struct {
	ID         int
	PID        uint16
	StartNanos int64 // PTS of the first sample on the global timeline
	CodecHint  uint8 // PMT stream type; 0 means "let ffmpeg sniff"
	Data       []byte
}

// Result is the canonical, deterministic PCM output for one segment. AAC is
// produced exactly once later, over the whole assembled program, so it is not
// part of the per-segment result and never double-encoded.
type Result struct {
	SegmentID  int
	StartNanos int64
	Samples    int    // PCM sample count at 48 kHz
	PCM        []byte // canonical S16LE mono PCM (deterministic hash input)
	PCMSHA256  [32]byte
}

// Converter runs bounded FFmpeg conversions.
type Converter struct {
	ffmpeg  string
	bitrate int
}

// NewConverter locates ffmpeg once and validates the target bitrate.
func NewConverter(bitrate int) (*Converter, error) {
	if bitrate <= 0 {
		return nil, clock.New(clock.CodeAudioSpec, "audio", -1, 0, 0,
			"bitrate must be positive, got %d", bitrate)
	}
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, clock.New(clock.CodeAudioFFmpeg, "audio", -1, 0, 0,
			"ffmpeg not found in PATH: %v", err)
	}
	return &Converter{ffmpeg: path, bitrate: bitrate}, nil
}

// Process decodes one elementary-stream segment and downmixes/resamples it to
// canonical mono 48 kHz S16LE PCM. Loudness normalization is deliberately NOT
// applied here: it runs once over the assembled program for an accurate
// integrated LUFS. Cancellation aborts the child without partial output.
func (c *Converter) Process(ctx context.Context, seg Segment) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, clock.New(clock.CodeCanceled, "audio", -1,
			seg.StartNanos, seg.StartNanos, "canceled before ffmpeg: %v", err)
	}
	pcm, err := c.decodeToPCM(ctx, seg.Data)
	if err != nil {
		return nil, err
	}
	return &Result{
		SegmentID:  seg.ID,
		StartNanos: seg.StartNanos,
		Samples:    len(pcm) / pcmBytesPerSample,
		PCM:        pcm,
		PCMSHA256:  sha256Sum(pcm),
	}, nil
}

// decodeToPCM turns ADTS AAC into mono 48 kHz S16LE, handling channel mapping.
func (c *Converter) decodeToPCM(ctx context.Context, in []byte) ([]byte, error) {
	filter := fmt.Sprintf(
		"aresample=%d,pan=%dc|c0=0.5*c0+0.5*c1", TargetRate, TargetChannels)
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "aac", "-i", "pipe:0",
		"-af", filter,
		"-ac", strconv.Itoa(TargetChannels),
		"-ar", strconv.Itoa(TargetRate),
		"-f", "s16le", "-y", "pipe:1",
	}
	out, err := c.run(ctx, args, in)
	if err != nil {
		return nil, err
	}
	if len(out)%pcmBytesPerSample != 0 {
		return nil, clock.New(clock.CodeAudioSpec, "audio", -1, 0, 0,
			"PCM output %d bytes is not sample aligned", len(out))
	}
	return out, nil
}

// NormalizeProgram applies a two-pass dynamic loudnorm to one continuous PCM
// program so the integrated loudness and true peak reliably hit their targets.
func (c *Converter) NormalizeProgram(ctx context.Context, pcm []byte) ([]byte, error) {
	measured, err := c.loudnormMeasure(ctx, pcm)
	if err != nil {
		return nil, err
	}
	filter := measured.ApplyFilter()
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "s16le", "-ar", strconv.Itoa(TargetRate),
		"-ac", strconv.Itoa(TargetChannels), "-i", "pipe:0",
		"-af", filter,
		"-f", "s16le", "-y", "pipe:1",
	}
	return c.run(ctx, args, pcm)
}

// loudnormMeasure runs the analysis pass on raw PCM and parses the JSON stats.
func (c *Converter) loudnormMeasure(ctx context.Context, pcm []byte) (loudStats, error) {
	filter := fmt.Sprintf(
		"loudnorm=I=%s:TP=%s:LRA=11:print_format=json",
		strconv.FormatFloat(TargetLUFS, 'f', 1, 64),
		strconv.FormatFloat(TargetTP, 'f', 1, 64),
	)
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "info",
		"-f", "s16le", "-ar", strconv.Itoa(TargetRate),
		"-ac", strconv.Itoa(TargetChannels), "-i", "pipe:0",
		"-af", filter, "-f", "null", "-",
	}
	cmd := exec.CommandContext(ctx, c.ffmpeg, args...)
	cmd.Stdin = bytes.NewReader(pcm)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return loudStats{}, clock.New(clock.CodeCanceled, "audio", -1, 0, 0,
				"ffmpeg canceled")
		}
		return loudStats{}, clock.New(clock.CodeAudioFFmpeg, "audio", -1, 0, 0,
			"loudnorm measure: %v: %s", err, stderr.String())
	}
	return parseLoudnormJSON(stderr.String())
}

// EncodeFMp4 encodes one continuous mono S16LE PCM buffer directly into a
// fragmented MP4 with the canonical 48 kHz media timescale. This is the single
// encoding pass, so the AAC bitrate stays on target and the timeline is intact.
func (c *Converter) EncodeFMp4(ctx context.Context, pcm []byte, outPath string) error {
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "s16le", "-ar", strconv.Itoa(TargetRate),
		"-ac", strconv.Itoa(TargetChannels), "-i", "pipe:0",
		"-c:a", "aac", "-b:a", strconv.Itoa(c.bitrate),
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4", "-y", outPath,
	}
	_, err := c.runToFile(ctx, args, pcm, outPath)
	return err
}

// DecodeFMp4 decodes a fragmented MP4 back to mono 48 kHz S16LE PCM so the
// delivered, encoded audio can be measured against its pre-encode source.
func (c *Converter) DecodeFMp4(ctx context.Context, inPath string) ([]byte, error) {
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "mp4", "-i", inPath,
		"-ac", strconv.Itoa(TargetChannels),
		"-ar", strconv.Itoa(TargetRate),
		"-f", "s16le", "-y", "pipe:1",
	}
	return c.runNoInput(ctx, args)
}

// runNoInput executes ffmpeg whose input is a file (no stdin payload).
func (c *Converter) runNoInput(ctx context.Context, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.ffmpeg, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, clock.New(clock.CodeCanceled, "audio", -1, 0, 0,
				"ffmpeg canceled")
		}
		return nil, clock.New(clock.CodeAudioFFmpeg, "audio", -1, 0, 0,
			"ffmpeg failed: %v: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// runToFile executes ffmpeg writing to a file path rather than stdout.
func (c *Converter) runToFile(ctx context.Context, args []string, stdin []byte, outPath string) (string, error) {
	cmd := exec.CommandContext(ctx, c.ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return "", clock.New(clock.CodeCanceled, "audio", -1, 0, 0,
				"ffmpeg canceled")
		}
		return "", clock.New(clock.CodeAudioFFmpeg, "audio", -1, 0, 0,
			"ffmpeg failed: %v: %s", err, stderr.String())
	}
	return outPath, nil
}

// run executes ffmpeg with stdin bytes and returns stdout. A nonzero exit or
// context cancellation is surfaced as a stable clock.CodeAudioFFmpeg error.
func (c *Converter) run(ctx context.Context, args []string, stdin []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.ffmpeg, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, clock.New(clock.CodeCanceled, "audio", -1, 0, 0,
				"ffmpeg canceled")
		}
		return nil, clock.New(clock.CodeAudioFFmpeg, "audio", -1, 0, 0,
			"ffmpeg %v failed: %v: %s", args[0], err, stderr.String())
	}
	return stdout.Bytes(), nil
}
