package main

import (
	"bytes"
	"context"
	"os/exec"

	"mediarun/internal/audio"
)

// encodeAAC renders a normalized WAV into an AAC MP4 so go-mp4 can read a real
// ISO base media container. It uses the same pinned ffmpeg binary as audio work.
func encodeAAC(ctx context.Context, n *audio.Normalizer, wavPath, mp4Path string) error {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, "-hide_banner", "-nostdin", "-y",
		"-i", wavPath, "-c:a", "aac", "-movflags", "+faststart", mp4Path)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return &ffmpegError{args: cmd.Args, cause: err, output: buf.String()}
	}
	return nil
}

type ffmpegError struct {
	args   []string
	cause  error
	output string
}

func (e *ffmpegError) Error() string {
	return "ffmpeg " + e.cause.Error() + ": " + e.output
}

func (e *ffmpegError) Unwrap() error { return e.cause }
