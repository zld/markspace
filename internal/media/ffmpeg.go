package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"

	"gonum.org/v1/gonum/stat"
)

// PerfectPSNR is reported for frames with zero error.
const PerfectPSNR = 99.0

// ErrFFmpegMissing means the ffmpeg binary is not on PATH.
var ErrFFmpegMissing = errors.New("ffmpeg binary not found on PATH")

// EncodeOptions configures one mpeg4 encode attempt.
type EncodeOptions struct {
	InputY4M  string
	OutputM4V string
	QP        int // mpeg4 quantizer, clamped to [2,31] by callers
}

// FrameStats aggregates per-frame PSNR statistics for one segment.
type FrameStats struct {
	Frames  int
	PSNRMean float64
	PSNRMin  float64
	PSNRMax  float64
	PSNRStd  float64
}

// EnsureFFmpeg verifies that ffmpeg is callable and supports the mpeg4 encoder.
func EnsureFFmpeg(ctx context.Context) error {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return ErrFFmpegMissing
	}
	cmd := exec.CommandContext(ctx, path, "-hide_banner", "-encoders")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg: cannot list encoders: %w", err)
	}
	if !bytes.Contains(out.Bytes(), []byte(" mpeg4 ")) {
		return errors.New("ffmpeg: mpeg4 encoder unavailable")
	}
	return nil
}

// runFFmpeg executes ffmpeg with an argument array (never a shell string),
// capturing stderr for diagnostics.
func runFFmpeg(ctx context.Context, args []string) error {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return ErrFFmpegMissing
	}
	full := append([]string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error"}, args...)
	cmd := exec.CommandContext(ctx, path, full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg %v: %w: %s", args, err, stderr.String())
	}
	return nil
}

// Encode runs one fixed-QP mpeg4 encode. It returns the output byte size.
func Encode(ctx context.Context, opts EncodeOptions) (int64, error) {
	if opts.QP < 2 || opts.QP > 31 {
		return 0, fmt.Errorf("encode: QP %d out of range [2,31]", opts.QP)
	}
	if _, err := os.Stat(opts.InputY4M); err != nil {
		return 0, fmt.Errorf("encode: input missing: %w", err)
	}
	args := []string{
		"-f", "yuv4mpegpipe", "-i", opts.InputY4M,
		"-an", "-sn",
		"-c:v", "mpeg4",
		"-q:v", strconv.Itoa(opts.QP),
		"-f", "m4v", opts.OutputM4V,
	}
	if err := runFFmpeg(ctx, args); err != nil {
		return 0, err
	}
	info, err := os.Stat(opts.OutputM4V)
	if err != nil {
		return 0, fmt.Errorf("encode: output missing after success: %w", err)
	}
	return info.Size(), nil
}

// Decode decodes an m4v bitstream back to a Y4M pipe file for comparison.
func Decode(ctx context.Context, m4vPath, outY4M string) error {
	if _, err := os.Stat(m4vPath); err != nil {
		return fmt.Errorf("decode: bitstream missing: %w", err)
	}
	args := []string{
		"-f", "m4v", "-i", m4vPath,
		"-an", "-sn",
		"-pix_fmt", "yuv420p",
		"-f", "yuv4mpegpipe", outY4M,
	}
	return runFFmpeg(ctx, args)
}

// CompareY4M streams source and decoded Y4M files and computes per-frame
// luma PSNR plus aggregate statistics. Header mismatches are rejected.
func CompareY4M(srcPath, decPath string) (FrameStats, error) {
	sf, err := os.Open(srcPath)
	if err != nil {
		return FrameStats{}, err
	}
	defer sf.Close()
	df, err := os.Open(decPath)
	if err != nil {
		return FrameStats{}, err
	}
	defer df.Close()
	src, err := NewReader(sf)
	if err != nil {
		return FrameStats{}, err
	}
	dec, err := NewReader(df)
	if err != nil {
		return FrameStats{}, err
	}
	sh, dh := src.Header(), dec.Header()
	if sh.Width != dh.Width || sh.Height != dh.Height || sh.FrameSize != dh.FrameSize {
		return FrameStats{}, fmt.Errorf("psnr: header mismatch %+v vs %+v", sh, dh)
	}
	sbuf := make([]byte, sh.FrameSize)
	dbuf := make([]byte, dh.FrameSize)
	yCount := sh.Width * sh.Height
	var psnrs []float64
	var mseSum float64
	for {
		serr := src.NextFrame(sbuf)
		derr := dec.NextFrame(dbuf)
		if errors.Is(serr, io.EOF) || errors.Is(derr, io.EOF) {
			if !errors.Is(serr, io.EOF) || !errors.Is(derr, io.EOF) {
				return FrameStats{}, errors.New("psnr: frame count mismatch between source and decoded streams")
			}
			break
		}
		if serr != nil {
			return FrameStats{}, fmt.Errorf("psnr: source: %w", serr)
		}
		if derr != nil {
			return FrameStats{}, fmt.Errorf("psnr: decoded: %w", derr)
		}
		var mse float64
		for i := 0; i < yCount; i++ {
			d := float64(sbuf[i]) - float64(dbuf[i])
			mse += d * d
		}
		mse /= float64(yCount)
		mseSum += mse
		if mse == 0 {
			psnrs = append(psnrs, PerfectPSNR)
		} else {
			psnrs = append(psnrs, 10*math.Log10(255*255/mse))
		}
	}
	if len(psnrs) == 0 {
		return FrameStats{}, errors.New("psnr: no frames compared")
	}
	minP, maxP := psnrs[0], psnrs[0]
	for _, p := range psnrs[1:] {
		if p < minP {
			minP = p
		}
		if p > maxP {
			maxP = p
		}
	}
	meanMSE := mseSum / float64(len(psnrs))
	overall := PerfectPSNR
	if meanMSE > 0 {
		overall = 10 * math.Log10(255*255/meanMSE)
	}
	return FrameStats{
		Frames:   len(psnrs),
		PSNRMean: overall,
		PSNRMin:  minP,
		PSNRMax:  maxP,
		PSNRStd:  stat.StdDev(psnrs, nil),
	}, nil
}
