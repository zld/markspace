package media

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// FixtureSpec describes a deterministically generated sample video.
type FixtureSpec struct {
	Width            int
	Height           int
	FPS              int
	Segments         int
	FramesPerSegment int
	TailFrames       int // short final chunk, deliberately shorter than a full segment
}

// FixtureManifest records what the fixture generator wrote, alongside the mp4.
type FixtureManifest struct {
	OutputPath       string `json:"output_path"`
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	FPS              int    `json:"fps"`
	TotalFrames      int    `json:"total_frames"`
	Segments         int    `json:"segments"`
	FramesPerSegment int    `json:"frames_per_segment"`
	TailFrames       int    `json:"tail_frames"`
}

// lcg is a deterministic linear-congruential generator, so fixture bytes are
// reproducible across runs and machines (no OS randomness, no online assets).
type lcg struct{ state uint64 }

func (l *lcg) next() uint64 {
	l.state = l.state*6364136223846793005 + 1442695040888963407
	return l.state
}

// GenerateFixture renders the sample video to workDir/source.mp4. Frames are
// synthesized in Go as raw yuv420p and piped into a single libx264 encode.
//
// Content phases (per full segment): static hold, fine line texture, grain
// noise, duplicated frames, moving gradient; the tail is a short static hold.
func GenerateFixture(ctx context.Context, workDir string, spec FixtureSpec) (FixtureManifest, error) {
	if err := validateFixtureSpec(spec); err != nil {
		return FixtureManifest{}, err
	}
	outPath := filepath.Join(workDir, "source.mp4")
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-y",
		"-f", "rawvideo", "-pix_fmt", "yuv420p",
		"-s", fmt.Sprintf("%dx%d", spec.Width, spec.Height),
		"-r", fmt.Sprintf("%d", spec.FPS),
		"-i", "pipe:0",
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "18",
		"-pix_fmt", "yuv420p", "-an",
		outPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return FixtureManifest{}, err
	}
	var stderr sink
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return FixtureManifest{}, fmt.Errorf("start ffmpeg for fixture: %w", err)
	}

	total := spec.Segments*spec.FramesPerSegment + spec.TailFrames
	rng := &lcg{state: 0x123456789abcdef0}
	frameSize := spec.Width * spec.Height * 3 / 2
	buf := make([]byte, frameSize)
	writeErr := make(chan error, 1)
	go func() {
		defer stdin.Close()
		for f := 0; f < total; f++ {
			renderFrame(buf, spec, f, rng)
			if err := writeAll(stdin, buf); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- nil
	}()

	waitErr := cmd.Wait()
	if werr := <-writeErr; werr != nil {
		return FixtureManifest{}, fmt.Errorf("write fixture frames: %w", werr)
	}
	if waitErr != nil {
		return FixtureManifest{}, fmt.Errorf("ffmpeg fixture encode failed: %w: %s", waitErr, stderr.String())
	}
	return FixtureManifest{
		OutputPath: outPath, Width: spec.Width, Height: spec.Height, FPS: spec.FPS,
		TotalFrames: total, Segments: spec.Segments,
		FramesPerSegment: spec.FramesPerSegment, TailFrames: spec.TailFrames,
	}, nil
}

// renderFrame paints frame f into the yuv420p buffer according to its phase.
func renderFrame(buf []byte, spec FixtureSpec, f int, rng *lcg) {
	w, h := spec.Width, spec.Height
	yPlane := buf[:w*h]
	seg := f / spec.FramesPerSegment
	within := f % spec.FramesPerSegment
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var v byte
			switch {
			case seg >= spec.Segments: // tail: static mid-gray hold
				v = 90
			case seg == 0: // static frame, identical every time
				v = staticLuma(x, y)
			case seg == 1: // fine vertical line texture, slowly panning
				v = lineLuma(x, y, within)
			case seg == 2: // grain noise
				v = byte(rng.next() >> 56)
			case seg == 3: // duplicate frames: same content for frame pairs
				v = gradientLuma(x, y, within/2)
			default: // moving gradient
				v = gradientLuma(x, y, within)
			}
			yPlane[y*w+x] = v
		}
	}
	// Neutral gray chroma (U=V=128); luma carries all complexity.
	for i := w * h; i < len(buf); i++ {
		buf[i] = 128
	}
}

func staticLuma(x, y int) byte {
	return byte(40 + (x*7+y*5)%180)
}

func lineLuma(x, y, t int) byte {
	if (x+t)%4 < 2 {
		return 235
	}
	return 25 + byte(y%30)
}

func gradientLuma(x, y, t int) byte {
	return byte((x + t*3) % 256)
}

func validateFixtureSpec(s FixtureSpec) error {
	switch {
	case s.Width <= 0 || s.Height <= 0 || s.Width%2 != 0 || s.Height%2 != 0:
		return fmt.Errorf("fixture: dimensions must be positive even numbers, got %dx%d", s.Width, s.Height)
	case s.FPS <= 0:
		return fmt.Errorf("fixture: fps must be positive, got %d", s.FPS)
	case s.Segments <= 0:
		return fmt.Errorf("fixture: segments must be positive, got %d", s.Segments)
	case s.FramesPerSegment <= 0:
		return fmt.Errorf("fixture: frames-per-segment must be positive")
	case s.TailFrames < 0 || s.TailFrames >= s.FramesPerSegment:
		return fmt.Errorf("fixture: tail frames must be in [0, frames-per-segment), got %d", s.TailFrames)
	}
	return nil
}

// sink captures subprocess stderr without letting it flood the terminal.
type sink struct{ b []byte }

func (s *sink) Write(p []byte) (int, error) {
	if len(s.b) < 4096 {
		s.b = append(s.b, p...)
	}
	return len(p), nil
}
func (s *sink) String() string {
	if len(s.b) > 2048 {
		return string(s.b[:2048]) + "...(truncated)"
	}
	return string(s.b)
}

// writeAll keeps writing until the whole buffer is consumed (pipe writes may
// accept fewer bytes than offered).
func writeAll(w io.Writer, buf []byte) error {
	for len(buf) > 0 {
		n, err := w.Write(buf)
		if err != nil {
			return err
		}
		buf = buf[n:]
	}
	return nil
}

// EnsureFileExists is a small guard used by callers before probing.
func EnsureFileExists(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("input path %s: %w", path, err)
	}
	return nil
}
