// Package audio drives the local FFmpeg binary to decode, resample, downmix,
// normalize and re-encode audio segments on the shared integer timeline.
// Every external invocation uses fixed argument lists built by this package;
// payload bytes are never passed through a shell or interpreted as commands.
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

// Segment is one independently processable chunk of elementary audio bytes.
type Segment struct {
	ID         int
	PID        uint16
	StartNanos int64 // PTS of the first sample on the global timeline
	CodecHint  uint8 // PMT stream type; 0 means "let ffmpeg sniff"
	Data       []byte
}

// Result is the normalized, encoded output for one segment.
type Result struct {
	SegmentID  int
	StartNanos int64
	Samples    int    // PCM sample count at 48 kHz
	PCM        []byte // normalized S16LE mono PCM (deterministic hash input)
	AAC        []byte // re-encoded AAC bitstream
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

// normalize decodes input to canonical S16LE mono 48 kHz PCM, applying the
// loudness and true-peak targets in one deterministic filter graph.
func (c *Converter) normalize(ctx context.Context, in []byte) ([]byte, error) {
	filter := fmt.Sprintf(
		"aresample=%d,pan=%dc|c0=0.5*c0+0.5*c1,loudnorm=I=%s:TP=%s:LRA=11",
		TargetRate, TargetChannels,
		strconv.FormatFloat(TargetLUFS, 'f', 1, 64),
		strconv.FormatFloat(TargetTP, 'f', 1, 64),
	)
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

// encodeAAC produces an ADTS AAC bitstream at the configured target bitrate.
func (c *Converter) encodeAAC(ctx context.Context, pcm []byte) ([]byte, error) {
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "s16le", "-ar", strconv.Itoa(TargetRate),
		"-ac", strconv.Itoa(TargetChannels), "-i", "pipe:0",
		"-c:a", "aac", "-b:a", strconv.Itoa(c.bitrate),
		"-f", "adts", "-y", "pipe:1",
	}
	return c.run(ctx, args, pcm)
}

// Process normalizes then re-encodes one segment, returning a self-contained
// result. Cancellation aborts the child processes without partial output.
func (c *Converter) Process(ctx context.Context, seg Segment) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, clock.New(clock.CodeCanceled, "audio", -1,
			seg.StartNanos, seg.StartNanos, "canceled before ffmpeg: %v", err)
	}
	pcm, err := c.normalize(ctx, seg.Data)
	if err != nil {
		return nil, err
	}
	aac, err := c.encodeAAC(ctx, pcm)
	if err != nil {
		return nil, err
	}
	return &Result{
		SegmentID:  seg.ID,
		StartNanos: seg.StartNanos,
		Samples:    len(pcm) / pcmBytesPerSample,
		PCM:        pcm,
		AAC:        aac,
		PCMSHA256:  sha256Sum(pcm),
	}, nil
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
