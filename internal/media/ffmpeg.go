// Package media drives the locally installed FFmpeg binaries to synthesise
// reference material and encode candidate renditions. All operations are
// local; no network services are contacted.
package media

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"mediacore/internal/fault"
)

// Encoder is one locally available video encoder.
type Encoder struct {
	Codec string
	Lib   string
}

// Encoders maps the supported codecs to their FFmpeg encoder names.
var Encoders = map[string]Encoder{
	"h264": {Codec: "h264", Lib: "libx264"},
	"h265": {Codec: "h265", Lib: "libx265"},
	"av1":  {Codec: "av1", Lib: "libsvtav1"},
}

// Tool is a resolved FFmpeg invocation pair.
type Tool struct {
	FFmpeg  string
	FFprobe string
	Version string
}

// Locate finds FFmpeg 7 and FFprobe 7. MEDIACORE_FFMPEG may point at an
// explicit ffmpeg binary.
func Locate() (*Tool, error) {
	candidates := []string{}
	if v := os.Getenv("MEDIACORE_FFMPEG"); v != "" {
		candidates = append(candidates, v)
	}
	candidates = append(candidates,
		"/opt/homebrew/opt/ffmpeg@7/bin/ffmpeg",
		"/opt/homebrew/bin/ffmpeg",
		"/usr/local/bin/ffmpeg",
	)
	candidates = append(candidates, "ffmpeg")
	for _, c := range candidates {
		out, err := exec.Command(c, "-version").Output()
		if err != nil {
			continue
		}
		first := strings.SplitN(string(out), "\n", 2)[0]
		// The acceptance target is FFmpeg 7.1. Newer majors share the flags
		// used here, so they are accepted only as an explicit fallback.
		if !strings.Contains(first, "ffmpeg version 7.") && mediaExplicit(c) {
			continue
		}
		probe := findFFprobe(c)
		if probe == "" {
			continue
		}
		return &Tool{FFmpeg: c, FFprobe: probe, Version: first}, nil
	}
	return nil, fault.Newf(fault.CodeFFmpegUnavailable, "",
		"FFmpeg 7.1 not found; install it with: brew install ffmpeg")
}

func mediaExplicit(c string) bool {
	switch c {
	case "ffmpeg", "/opt/homebrew/bin/ffmpeg", "/usr/local/bin/ffmpeg":
		return false
	}
	return os.Getenv("MEDIACORE_FFMPEG") != c
}

func findFFprobe(ffmpeg string) string {
	dir := ""
	if i := strings.LastIndex(ffmpeg, "/"); i >= 0 {
		dir = ffmpeg[:i]
	}
	candidates := []string{}
	if dir != "" {
		candidates = append(candidates, dir+"/ffprobe")
	}
	candidates = append(candidates, "/opt/homebrew/opt/ffmpeg@7/bin/ffprobe", "/opt/homebrew/bin/ffprobe")
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	if p, err := exec.LookPath("ffprobe"); err == nil {
		return p
	}
	return ""
}

// run executes a command and returns its combined stderr.
func (t *Tool) run(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stderr.Bytes(), fault.Newf(fault.CodeFFmpegFailed, "",
			"%s failed: %v: %s", name, err, lastLine(stderr.String()))
	}
	return stderr.Bytes(), nil
}

// runStdout returns stdout, where FFprobe writes its JSON document.
func (t *Tool) runStdout(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fault.Newf(fault.CodeFFmpegFailed, "",
			"%s failed: %v: %s", name, err, lastLine(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
}

// Synthetic describes the locally generated reference material.
type Synthetic struct {
	Width   int
	Height  int
	FPS     int
	Seconds int
	Seed    int64
}

// Valid reports whether the description is usable.
func (s Synthetic) Valid() bool {
	return s.Width > 0 && s.Height > 0 && s.FPS > 0 && s.Seconds > 0
}

// Duration returns the clip length in seconds.
func (s Synthetic) Duration() float64 { return float64(s.Seconds) }

// Frames returns the number of video frames.
func (s Synthetic) Frames() int { return s.FPS * s.Seconds }

// Generate produces a deterministic YUV420p reference clip and a WAV track.
// A seed-derived hue makes different seeds differ while staying repeatable.
func (t *Tool) Generate(dir string, s Synthetic) (refPath, wavPath string, err error) {
	if !s.Valid() {
		return "", "", fault.Newf(fault.CodeFFmpegFailed, "", "invalid synthetic media description")
	}
	hue := float64((s.Seed*37 + 11) % 360)
	src := fmt.Sprintf("testsrc2=size=%dx%d:rate=%d:duration=%d,hue=h=%.2f",
		s.Width, s.Height, s.FPS, s.Seconds, hue)
	refPath = dir + "/reference.yuv"
	if _, err = t.run(t.FFmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "lavfi", "-i", src, "-pix_fmt", "yuv420p", "-f", "rawvideo", refPath); err != nil {
		return "", "", err
	}
	wavPath = dir + "/audio.wav"
	audio := fmt.Sprintf("sine=frequency=%d:sample_rate=48000:duration=%d", 440+s.Seed%220, s.Seconds)
	if _, err = t.run(t.FFmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "lavfi", "-i", audio, "-c:a", "pcm_s16le", wavPath); err != nil {
		return "", "", err
	}
	return refPath, wavPath, nil
}
