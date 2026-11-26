// Package audio runs the decode -> resample -> layout -> loudness
// pipeline. Decoding and encoding are delegated to a local FFmpeg
// binary (no network, no cgo); all sample math is pure Go so the
// loudness and resampling rules stay unit-testable.
package audio

import (
	"audionorm/internal/media"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

// DecoderVersion is the codec-pipeline version stamped into checkpoints.
// Any change to decode/resample/loudness rules bumps this string, which
// invalidates downstream stages on restart.
const DecoderVersion = "audionorm-decode-1"

// PCM is interleaved stereo float32 PCM on the unified 48 kHz clock.
// Frame i occupies Samples[2*i] (left) and Samples[2*i+1] (right).
type PCM struct {
	Rate     uint32
	Channels uint16
	Samples  []float32
}

// Frames returns the number of stereo frames.
func (p *PCM) Frames() int { return len(p.Samples) / 2 }

// EncodeF32LE serializes the samples as little-endian float32 bytes.
func (p *PCM) EncodeF32LE() []byte {
	buf := bytes.NewBuffer(make([]byte, 0, len(p.Samples)*4))
	_ = binary.Write(buf, binary.LittleEndian, p.Samples)
	return buf.Bytes()
}

// Decoder shells out to FFmpeg to turn one coded packet into raw
// float32 PCM. FFmpeg performs decode, native resampling to 48 kHz and
// channel up/down-mix to stereo; the Go side validates and measures.
type Decoder struct {
	binaryPath string
}

// NewDecoder locates the ffmpeg executable via PATH.
func NewDecoder() (*Decoder, error) {
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("ffmpeg not found on PATH (need >= 7.1): %w", err)
	}
	return &Decoder{binaryPath: p}, nil
}

// DecodePacket decodes one RTP-style coded packet. codec is "opus" or
// "aac-lc"; srcRate and srcChannels describe the coded stream. The
// returned PCM is always 48 kHz stereo.
func (d *Decoder) DecodePacket(ctx context.Context, codec string, srcRate uint32, srcChannels uint16, payload []byte) (*PCM, error) {
	if len(payload) == 0 {
		return nil, errors.New("empty coded payload")
	}
	if len(payload) > media.MaxPacketBytes {
		return nil, errors.New("packet exceeds 1 MiB limit")
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-f"}
	switch codec {
	case "opus":
		// Raw Opus packets require an Ogg/Matroska container; use Matroska
		// only at the stream level in DecodeStream. Single raw packets are
		// wrapped by the caller through DecodeStream instead.
		return nil, errors.New("raw single-opus decode must use DecodeStream")
	case "aac-lc":
		// FFmpeg's raw ADTS demuxer is named "aac" (the "adts" muxer
		// produces the same bitstream).
		args = append(args, "aac")
	default:
		return nil, fmt.Errorf("unsupported codec %q", codec)
	}
	args = append(args, "-i", "pipe:0",
		"-ar", "48000", "-ac", "2",
		"-f", "f32le", "-acodec", "pcm_f32le", "pipe:1")
	return d.runFFmpeg(ctx, args, payload)
}

// StreamSpec describes one contiguous coded stream handed to FFmpeg.
type StreamSpec struct {
	Codec     string // "opus" | "aac-lc"
	Container string // "matroska" (opus) or "aac" (raw ADTS, FFmpeg demuxer name)
	SrcRate   uint32
	Channels  uint16
}

// DecodeStream decodes a whole containerized byte stream into one PCM
// block. It is the streaming-friendly path used by the task pipeline;
// packet boundaries inside the container are FFmpeg's responsibility.
func (d *Decoder) DecodeStream(ctx context.Context, spec StreamSpec, data []byte) (*PCM, error) {
	if spec.Channels != 1 && spec.Channels != 2 {
		return nil, fmt.Errorf("%w: %d channels", errors.New("unsupported layout"), spec.Channels)
	}
	demuxer := spec.Container
	if spec.Container == "adts" {
		demuxer = "aac" // normalize the common alias to FFmpeg's demuxer name
	}
	args := []string{"-hide_banner", "-loglevel", "error",
		"-f", demuxer, "-i", "pipe:0",
		"-ar", "48000", "-ac", "2",
		"-f", "f32le", "-acodec", "pcm_f32le", "pipe:1"}
	pcm, err := d.runFFmpeg(ctx, args, data)
	if err != nil {
		return nil, err
	}
	pcm.Rate = media.TimelineRate
	pcm.Channels = 2
	return pcm, nil
}

func (d *Decoder) runFFmpeg(ctx context.Context, args []string, stdin []byte) (*PCM, error) {
	cmd := exec.CommandContext(ctx, d.binaryPath, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg failed: %w: %s", err, stderr.String())
	}
	raw := stdout.Bytes()
	if len(raw)%4 != 0 {
		return nil, errors.New("ffmpeg returned non-float32-aligned output")
	}
	samples := make([]float32, len(raw)/4)
	br := bytes.NewReader(raw)
	if err := binary.Read(br, binary.LittleEndian, samples); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return &PCM{Rate: media.TimelineRate, Channels: 2, Samples: samples}, nil
}
