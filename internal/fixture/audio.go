package fixture

import (
	"bytes"
	"context"
	"os/exec"

	"rtpregress/internal/clock"
)

// GenerateAAC synthesizes a deterministic mono AAC ADTS stream of the given
// duration using only the local ffmpeg binary (a swept sine, no input files).
func GenerateAAC(ctx context.Context, ffmpeg string, seconds int) ([][]byte, error) {
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i",
		"sine=frequency=440:sample_rate=48000:duration=" + itoa(seconds),
		"-ac", "1", "-c:a", "aac", "-b:a", "96k",
		"-f", "adts", "-y", "pipe:1",
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, clock.New(clock.CodeAudioFFmpeg, "fixture", -1, 0, 0,
			"aac generation: %v: %s", err, stderr.String())
	}
	return splitADTS(stdout.Bytes()), nil
}

// splitADTS breaks an ADTS byte stream into individual access units on sync
// words (0xFFF). Each returned slice is one independently muxable frame.
func splitADTS(b []byte) [][]byte {
	var frames [][]byte
	i := 0
	for i+7 <= len(b) {
		if b[i] != 0xFF || b[i+1]&0xF0 != 0xF0 {
			i++
			continue
		}
		// frame_length is 13 bits across header bytes 3..5
		frameLen := int(b[i+3]&0x03)<<11 | int(b[i+4])<<3 | int(b[i+5]>>5)
		if frameLen <= 0 || i+frameLen > len(b) {
			break
		}
		frames = append(frames, b[i:i+frameLen])
		i += frameLen
	}
	return frames
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
