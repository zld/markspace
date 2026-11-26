// Package media runs local FFmpeg raw decoders and stream-copy cuts. It does
// not access network services.
package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os/exec"
)

// FrameHandler receives one decoded RGB frame. The byte slice is reused and
// must not be retained beyond the callback.
type FrameHandler func(index int, ptsNS int64, rgb []byte) error

// DecodeRGB decodes path to contiguous rgb24 frames and invokes handle in
// decode order. PTS is deterministic frame-index PTS at fps; generated
// fixtures are constant frame rate.
func DecodeRGB(ctx context.Context, path string, width, height, fps int, handle FrameHandler) error {
	if width <= 0 || height <= 0 || fps <= 0 {
		return fmt.Errorf("invalid video format %dx%d@%d", width, height, fps)
	}
	frameSize := width * height * 3
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-i", path,
		"-f", "rawvideo", "-pix_fmt", "rgb24", "-")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg video decode: %w", err)
	}
	buf := make([]byte, frameSize)
	idx := 0
	for {
		_, err := io.ReadFull(stdout, buf)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			_ = cmd.Wait()
			return fmt.Errorf("read raw video frame: %w (%s)", err, stderr.String())
		}
		pts := int64(idx) * 1_000_000_000 / int64(fps)
		if err := handle(idx, pts, buf); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return err
		}
		idx++
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("ffmpeg video decode: %w (%s)", err, stderr.String())
	}
	return nil
}

// DecodeS16Mono decodes and resamples audio to mono signed 16-bit little
// endian PCM at sampleRate.
func DecodeS16Mono(ctx context.Context, path string, sampleRate int) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-i", path,
		"-vn", "-ac", "1", "-ar", fmt.Sprint(sampleRate),
		"-f", "s16le", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg audio decode: %w (%s)", err, stderr.String())
	}
	if len(b)%2 == 1 {
		b = b[:len(b)-1]
	}
	return b, nil
}

// CutWebM makes a stream-copy slice. Fixture cut points are keyframes, so
// input seeking is frame accurate.
func CutWebM(ctx context.Context, src, dst string, startNS, endNS int64) error {
	start := seconds(startNS)
	duration := seconds(endNS - startNS)
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-ss", start,
		"-i", src,
		"-t", duration,
		"-c", "copy",
		"-avoid_negative_ts", "make_zero",
		"-f", "webm", dst)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg cut [%s,%s): %w (%s)", start, duration, err, stderr.String())
	}
	return nil
}

func seconds(ns int64) string {
	return fmt.Sprintf("%.6f", float64(ns)/1_000_000_000.0)
}

// Int16Samples converts raw s16le bytes to native int values.
func Int16Samples(pcm []byte) []int {
	n := len(pcm) / 2
	out := make([]int, n)
	for i := 0; i < n; i++ {
		out[i] = int(int16(binary.LittleEndian.Uint16(pcm[2*i : 2*i+2])))
	}
	return out
}
