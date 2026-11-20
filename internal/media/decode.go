package media

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"time"
)

// DecodeOptions controls the FFmpeg subprocess.
type DecodeOptions struct {
	FFmpegPath string
	// ChunkFrames is the number of frames handed to the sink per call.
	ChunkFrames int
	// Timeout bounds the whole decode. Zero uses DefaultDecodeTimeout.
	Timeout time.Duration
	// MaxSeconds bounds the decoded duration; zero uses DefaultMaxSeconds and
	// a negative value disables the check.
	MaxSeconds float64
}

const (
	// DefaultDecodeTimeout keeps a wedged subprocess from stalling a batch.
	DefaultDecodeTimeout = 10 * time.Minute
	defaultChunkFrames   = 4096
)

// DecodeError records the stage and reason for a decoding failure.
type DecodeError struct {
	Stage  string
	Reason string
	Err    error
}

func (e *DecodeError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Stage, e.Reason, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Stage, e.Reason)
}

func (e *DecodeError) Unwrap() error { return e.Err }

// Sink consumes interleaved float64 PCM. It must not retain the slice after
// returning; the decoder reuses the buffer.
type Sink func(frames []float64) error

// Decode streams the first audio stream of path as interleaved little-endian
// float32 PCM through FFmpeg and converts each chunk to float64 for sink.
// FFmpeg is used only as a decoder: all measurement happens in Go.
func Decode(ctx context.Context, path string, info Info, opts DecodeOptions, sink Sink) error {
	if opts.FFmpegPath == "" {
		opts.FFmpegPath = "ffmpeg"
	}
	if opts.ChunkFrames <= 0 {
		opts.ChunkFrames = defaultChunkFrames
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultDecodeTimeout
	}
	maxSeconds := opts.MaxSeconds
	if maxSeconds == 0 {
		maxSeconds = DefaultMaxSeconds
	}
	if maxSeconds > 0 && info.Duration > maxSeconds*1.05 {
		return &DecodeError{Stage: "decode", Reason: fmt.Sprintf("duration %.1f s exceeds the %.1f s limit", info.Duration, maxSeconds)}
	}

	runCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, opts.FFmpegPath,
		"-nostdin",
		"-v", "error",
		"-i", path,
		"-map", "0:a:0",
		"-vn", "-sn", "-dn",
		"-acodec", "pcm_f32le",
		"-f", "f32le",
		"-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return &DecodeError{Stage: "decode", Reason: "cannot open ffmpeg stdout", Err: err}
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return &DecodeError{Stage: "decode", Reason: "ffmpeg not found", Err: err}
		}
		return &DecodeError{Stage: "decode", Reason: "cannot start ffmpeg", Err: err}
	}

	ch := info.Channels
	samples := make([]float64, opts.ChunkFrames*ch)
	// raw is read in chunkBytes-sized reads but allocated with room for one
	// frame of carry bytes, so a partial frame from the previous read can be
	// shifted to the front without allocating per iteration.
	chunkBytes := opts.ChunkFrames * ch * 4
	raw := make([]byte, chunkBytes+ch*4)
	// carry holds the bytes of an incomplete frame left by the previous read;
	// it is always shorter than one frame.
	carry := 0
	var decodedFrames int64
	maxFrames := int64(0)
	if maxSeconds > 0 {
		maxFrames = int64(maxSeconds*float64(info.SampleRate)) + int64(info.SampleRate)
	}

	reader := bufio.NewReaderSize(stdout, 256<<10)
	var readErr error
	for readErr == nil {
		var n int
		n, readErr = reader.Read(raw[carry : carry+chunkBytes])
		if n > 0 {
			buf := raw[:carry+n]
			whole := len(buf) / 4 * 4
			frames := whole / 4 / ch
			if frames > 0 {
				if err := convertLEFloat32(samples[:frames*ch], buf[:whole]); err != nil {
					kill(cmd)
					return &DecodeError{Stage: "decode", Reason: "invalid pcm sample", Err: err}
				}
				if err := sink(samples[:frames*ch]); err != nil {
					kill(cmd)
					return err
				}
				decodedFrames += int64(frames)
				if maxFrames > 0 && decodedFrames > maxFrames {
					kill(cmd)
					return &DecodeError{Stage: "decode", Reason: fmt.Sprintf("decoded more than %.1f s", maxSeconds)}
				}
			}
			// Consume exactly the frames handed to the sink. Bytes past that
			// point are a partial frame and must be carried: dropping the
			// whole-sample-but-partial-frame tail would shift the channel
			// interleave for every subsequent sample.
			consumed := frames * ch * 4
			carry = copy(raw, buf[consumed:])
		}
	}
	if carry != 0 {
		kill(cmd)
		return &DecodeError{Stage: "decode", Reason: "pcm stream ended mid-frame"}
	}
	waitErr := cmd.Wait()
	if runCtx.Err() == context.DeadlineExceeded {
		return &DecodeError{Stage: "decode", Reason: fmt.Sprintf("ffmpeg timed out after %s", opts.Timeout)}
	}
	if ctx.Err() != nil {
		return &DecodeError{Stage: "decode", Reason: "cancelled", Err: ctx.Err()}
	}
	if readErr != io.EOF {
		return &DecodeError{Stage: "decode", Reason: "reading pcm stream", Err: readErr}
	}
	if waitErr != nil {
		return &DecodeError{Stage: "decode", Reason: "ffmpeg failed", Err: fmt.Errorf("%v: %s", waitErr, tail(stderr.String()))}
	}
	if decodedFrames == 0 {
		return &DecodeError{Stage: "decode", Reason: "decoder produced no audio"}
	}
	return nil
}

func kill(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
}

// convertLEFloat32 converts little-endian float32 samples to float64 and
// rejects non-finite values, so downstream DSP never sees NaN or Inf.
func convertLEFloat32(dst []float64, src []byte) error {
	for i := range dst {
		bits := binary.LittleEndian.Uint32(src[i*4:])
		v := float64(math.Float32frombits(bits))
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("sample %d is not finite", i)
		}
		dst[i] = v
	}
	return nil
}
