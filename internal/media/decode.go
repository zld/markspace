package media

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// DecodeFrames decodes a frame range [start, start+count) from path into raw
// yuv420p planes. Frame-number alignment between source and segment outputs is
// the caller's responsibility; mismatches must never be silently skipped.
func DecodeFrames(ctx context.Context, path string, width, height, start, count int) ([][]byte, error) {
	if count <= 0 {
		return nil, fmt.Errorf("decode: frame count must be positive, got %d", count)
	}
	frameSize := width * height * 3 / 2
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-v", "error",
		"-select_streams", "v:0",
		"-i", path,
		"-vf", fmt.Sprintf("select='between(n,%d,%d)',setpts=N/FRAME_RATE/TB", start, start+count-1),
		"-frames:v", fmt.Sprintf("%d", count),
		"-f", "rawvideo", "-pix_fmt", "yuv420p",
		"pipe:1")
	var stderr sink
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg decode %s frames [%d,%d): %w: %s",
			path, start, start+count, explainExit(err), stderr.String())
	}
	if len(raw) < frameSize*count {
		return nil, fmt.Errorf("decode: expected %d bytes for %d frames, got %d (frame count mismatch)",
			frameSize*count, count, len(raw))
	}
	frames := make([][]byte, count)
	for i := 0; i < count; i++ {
		frames[i] = bytes.Clone(raw[i*frameSize : (i+1)*frameSize])
	}
	return frames, nil
}

// ProbeFrameCount re-decodes the full stream and counts frames, used as a
// cross-check that source and encoded output truly carry the same frames.
func ProbeFrameCount(ctx context.Context, path string) (int, error) {
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error", "-select_streams", "v:0",
		"-count_frames", "-show_entries", "stream=nb_read_frames",
		"-of", "csv=p=0", path)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("count frames in %s: %w", path, explainExit(err))
	}
	var n int
	if _, err := fmt.Sscanf(string(bytes.TrimSpace(out)), "%d", &n); err != nil {
		return 0, fmt.Errorf("parse frame count %q: %w", string(out), err)
	}
	return n, nil
}
