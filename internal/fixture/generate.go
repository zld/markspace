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
	ReplacePath string // valid but visually different fMP4 for the replace scenario
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
		MP4Path:     filepath.Join(dir, "source.fmp4"),
		ReplacePath: filepath.Join(dir, "replace.fmp4"),
		VTTPath:     filepath.Join(dir, "subs.vtt"), ChapterPath: filepath.Join(dir, "chapters.txt"),
		ID3Path: filepath.Join(dir, "tags.id3"), RTPPath: filepath.Join(dir, "sei.rtp"),
		TargetKbps: opt.TargetKbps, DurationSec: opt.DurationSec,
	}
	// High-entropy synthetic source: the moving test pattern plus temporal
	// noise gives the encoder enough detail that a real bitrate target is
	// achievable, so the acceptance bitrate check is meaningful.
	src := fmt.Sprintf("testsrc=duration=%.3f:size=%dx%d:rate=25,noise=alls=18:allf=t",
		opt.DurationSec, opt.Width, opt.Height)
	// Reference: visually lossless, still a local synthetic source.
	if err := ff.Run("-f", "lavfi", "-i", src, "-pix_fmt", "yuv420p",
		"-c:v", "libx264", "-crf", "18", "-preset", "veryfast", b.RefPath); err != nil {
		return nil, err
	}
	// Scanned asset: fragmented MP4 at the target bitrate via two-pass rate
	// control so the delivered bitrate tracks the target within a few percent.
	if err := twoPassFragmented(ff, src, opt, filepath.Join(dir, "pass-main"), b.MP4Path); err != nil {
		return nil, err
	}
	// Replacement asset: a valid, decodable fMP4 from a visually different
	// source (SMPTE bars + noise). It models a genuine same-path replacement:
	// new source hash and track content, so the index must rebuild and the
	// derived cache must miss.
	replaceSrc := fmt.Sprintf("smptebars=duration=%.3f:size=%dx%d:rate=25,noise=alls=12:allf=t",
		opt.DurationSec, opt.Width, opt.Height)
	if err := twoPassFragmented(ff, replaceSrc, opt, filepath.Join(dir, "pass-repl"), b.ReplacePath); err != nil {
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

// twoPassFragmented encodes a lavfi source into a fragmented MP4 at the target
// bitrate using two-pass rate control. -f mp4 is explicit because the .fmp4
// extension is not muxer-probeable.
func twoPassFragmented(ff FFmpeg, src string, opt Options, passLog, out string) error {
	// At 25 fps, a 25-frame GOP yields a keyframe once per second, giving the
	// recovery layer several independent sync boundaries inside the clip.
	common := []string{
		"-f", "lavfi", "-i", src, "-pix_fmt", "yuv420p",
		"-c:v", "libx264", "-b:v", fmt.Sprintf("%dk", opt.TargetKbps),
		"-preset", "veryfast",
		"-g", "25", "-keyint_min", "25", "-sc_threshold", "0",
	}
	if err := ff.Run(append(append([]string{}, common...),
		"-pass", "1", "-passlogfile", passLog, "-an", "-f", "mp4", os.DevNull)...); err != nil {
		return err
	}
	return ff.Run(append(append([]string{}, common...),
		"-pass", "2", "-passlogfile", passLog,
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4", out)...)
}
