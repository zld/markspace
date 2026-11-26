package demo

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
)

// EncodedStream holds the coded packets produced by the local FFmpeg.
type EncodedStream struct {
	Codec         string // "opus" | "aac-lc"
	Rate          uint32
	Channels      uint16
	Container     string // "ogg" | "adts"
	ContainerData []byte // full muxed byte stream for the FFmpeg demuxer
	Packets       [][]byte
}

// GenerateOpus encodes seconds of a 440 Hz tone to Ogg Opus and returns
// the raw Opus packets (headers stripped) plus the full Ogg container.
func GenerateOpus(ctx context.Context, seconds float64, channels uint16) (*EncodedStream, error) {
	args := []string{"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=" + strconv.FormatFloat(seconds, 'f', 1, 64),
		"-c:a", "libopus", "-ar", "48000", "-ac", strconv.Itoa(int(channels)),
		"-f", "ogg", "pipe:1"}
	data, err := runFFmpeg(ctx, args)
	if err != nil {
		return nil, err
	}
	pkts, err := ParseOgg(data)
	if err != nil {
		return nil, err
	}
	return &EncodedStream{
		Codec: "opus", Rate: 48000, Channels: channels,
		Container: "ogg", ContainerData: data, Packets: pkts,
	}, nil
}

// GenerateAAC encodes seconds of a tone to ADTS AAC-LC and returns raw
// access units at the requested rate and channel count (1 or 2).
func GenerateAAC(ctx context.Context, seconds float64, rate uint32, channels uint16) (*EncodedStream, error) {
	if channels != 1 && channels != 2 {
		return nil, errors.New("AAC demo only supports mono or stereo")
	}
	args := []string{"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=" + strconv.FormatFloat(seconds, 'f', 1, 64),
		"-c:a", "aac", "-b:a", "128k", "-ar", strconv.Itoa(int(rate)),
		"-ac", strconv.Itoa(int(channels)), "-f", "adts", "pipe:1"}
	data, err := runFFmpeg(ctx, args)
	if err != nil {
		return nil, err
	}
	pkts, err := SplitADTS(data)
	if err != nil {
		return nil, err
	}
	return &EncodedStream{
		Codec: "aac-lc", Rate: rate, Channels: channels,
		Container: "adts", ContainerData: data, Packets: pkts,
	}, nil
}

// Join returns the raw coded packets concatenated (without container).
func (e *EncodedStream) Join() []byte {
	var out bytes.Buffer
	for _, p := range e.Packets {
		out.Write(p)
	}
	return out.Bytes()
}

func runFFmpeg(ctx context.Context, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, errors.New("ffmpeg: " + err.Error() + ": " + stderr.String())
	}
	return stdout.Bytes(), nil
}
