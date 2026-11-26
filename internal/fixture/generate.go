// Package fixture generates all local sample material with the bundled ffmpeg
// binary and pure-Go metadata synthesis. No external assets are fetched: the
// video is a deterministic testsrc pattern and every sidecar (WebVTT, chapters,
// ID3, RTP-carried SEI, injected emsg) is produced in-process so scenarios are
// reproducible byte-for-byte.
package fixture

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// FFmpeg is the narrow command surface the generator needs. It is an interface
// so tests can substitute a recording runner without touching the real binary.
type FFmpeg interface {
	Run(args ...string) error
}

// LocalFFmpeg shells out to an ffmpeg binary on PATH.
type LocalFFmpeg struct{ Bin string }

func (l LocalFFmpeg) bin() string {
	if l.Bin == "" {
		return "ffmpeg"
	}
	return l.Bin
}

// Run executes ffmpeg with -y and fails on any non-zero exit, surfacing the
// tail of stderr for terminal diagnostics.
func (l LocalFFmpeg) Run(args ...string) error {
	full := append([]string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y"}, args...)
	cmd := exec.Command(l.bin(), full...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("fixture: ffmpeg %v: %w: %s", args, err, tail(out))
	}
	return nil
}

func tail(b []byte) string {
	s := string(b)
	if len(s) > 300 {
		s = "..." + s[len(s)-300:]
	}
	return s
}

// Bundle lists every artifact generated for one demo scenario family.
type Bundle struct {
	Dir         string
	RefPath     string // high-quality reference used for PSNR
	MP4Path     string // fragmented MP4 actually scanned
	VTTPath     string
	ChapterPath string
	ID3Path     string
	RTPPath     string // concatenated RTP packets carrying SEI
	TargetKbps  int
	DurationSec float64
}

// Options controls the base generated material.
type Options struct {
	DurationSec float64
	TargetKbps  int
	Width       int
	Height      int
}

// DefaultOptions returns small, fast-encoding material sized for local checks.
func DefaultOptions() Options {
	return Options{DurationSec: 6, TargetKbps: 800, Width: 320, Height: 240}
}

// GenerateBase produces the reference clip and a fragmented MP4 derived from
// it, plus the deterministic text/binary metadata sidecars.
func GenerateBase(ff FFmpeg, dir string, opt Options) (*Bundle, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	b := &Bundle{
		Dir: dir, RefPath: filepath.Join(dir, "ref.mp4"),
		MP4Path: filepath.Join(dir, "source.fmp4"),
		VTTPath: filepath.Join(dir, "subs.vtt"), ChapterPath: filepath.Join(dir, "chapters.txt"),
		ID3Path: filepath.Join(dir, "tags.id3"), RTPPath: filepath.Join(dir, "sei.rtp"),
		TargetKbps: opt.TargetKbps, DurationSec: opt.DurationSec,
	}
	src := fmt.Sprintf("testsrc=duration=%.3f:size=%dx%d:rate=25",
		opt.DurationSec, opt.Width, opt.Height)
	// Reference: visually lossless, still a local synthetic source.
	if err := ff.Run("-f", "lavfi", "-i", src, "-pix_fmt", "yuv420p",
		"-c:v", "libx264", "-crf", "18", "-preset", "veryfast", b.RefPath); err != nil {
		return nil, err
	}
	// Scanned asset: fragmented MP4 at the target bitrate. -f mp4 is explicit
	// because the .fmp4 extension is not auto-recognized by the muxer probe.
	if err := ff.Run("-f", "lavfi", "-i", src, "-pix_fmt", "yuv420p",
		"-c:v", "libx264", "-b:v", fmt.Sprintf("%dk", opt.TargetKbps),
		"-preset", "veryfast",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4", b.MP4Path); err != nil {
		return nil, err
	}
	if err := writeWebVTT(b.VTTPath, opt.DurationSec); err != nil {
		return nil, err
	}
	if err := writeChapters(b.ChapterPath); err != nil {
		return nil, err
	}
	if err := writeID3(b.ID3Path); err != nil {
		return nil, err
	}
	if err := writeRTPSEI(b.RTPPath, opt.DurationSec); err != nil {
		return nil, err
	}
	return b, nil
}
