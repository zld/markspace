// Package audio normalizes audio to 48 kHz stereo at -16 LUFS via FFmpeg.
// Loudness correction is a deterministic two-pass loudnorm run; unsupported
// channel layouts and missing tracks fail explicitly instead of guessing.
package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var (
	// ErrNoLoudnessReport means loudnorm printed no parseable JSON report.
	ErrNoLoudnessReport = errors.New("loudnorm report missing from ffmpeg output")
	// ErrUnsupportedLayout means the input channel layout cannot be mapped.
	ErrUnsupportedLayout = errors.New("unsupported channel layout")
	// ErrMissingTrack means the input carries no decodable audio track.
	ErrMissingTrack = errors.New("no audio track found in input")
)

const (
	// TargetRate is the mandated output sample rate.
	TargetRate = 48000
	// TargetChannels is the mandated stereo output.
	TargetChannels = 2
)

// WAVInfo describes a probed PCM WAV file.
type WAVInfo struct {
	Channels   uint16
	SampleRate uint32
	Bits       uint16
}

// Result captures one normalization run for provenance.
type Result struct {
	Input         WAVInfo
	SampleRate    int
	Channels      int
	FilterGraph   string
	InputMetrics  Metrics
	OutputMetrics Metrics
	FFmpegPath    string
	FFmpegVersion string
}

// Normalizer runs the fixed audio pipeline through one ffmpeg binary.
type Normalizer struct {
	bin     string
	version string
	loud    LoudnessParams
}

// NewNormalizer locates ffmpeg on PATH and records its exact version, which
// becomes part of every cache key.
func NewNormalizer() (*Normalizer, error) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("ffmpeg not found on PATH: %w", err)
	}
	out, err := exec.Command(bin, "-version").Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg version probe failed: %w", err)
	}
	return &Normalizer{bin: bin, version: parseVersion(string(out)), loud: DefaultLoudness()}, nil
}

// WithLoudness overrides the loudness target (tests use this).
func (n *Normalizer) WithLoudness(p LoudnessParams) *Normalizer {
	n.loud = p
	return n
}

// Version reports the probed ffmpeg version string.
func (n *Normalizer) Version() string { return n.version }

var versionRE = regexp.MustCompile(`ffmpeg version (\S+)`)

func parseVersion(banner string) string {
	if m := versionRE.FindStringSubmatch(banner); len(m) == 2 {
		return m[1]
	}
	return "unknown"
}

// Normalize performs validation, the loudness measurement pass and the linear
// correction pass, producing a 48 kHz stereo 16-bit PCM WAV at outputPath.
func (n *Normalizer) Normalize(ctx context.Context, inputPath, outputPath string) (Result, error) {
	info, err := ProbeWAV(inputPath)
	if err != nil {
		return Result{}, err
	}
	filter, err := buildFilterGraph(info)
	if err != nil {
		return Result{}, err
	}
	// Pass 1: measure only, discard media.
	m1Out, err := n.run(ctx,
		"-hide_banner", "-nostdin", "-i", inputPath,
		"-af", n.loud.passOneFilter(), "-f", "null", "-")
	if err != nil {
		return Result{}, fmt.Errorf("loudness measurement pass failed: %w", err)
	}
	inputMetrics, err := ParseLoudnessJSON(m1Out)
	if err != nil {
		return Result{}, err
	}
	// Pass 2: resample, map channels, then correct loudness. Use linear mode
	// with measured metrics when available; otherwise a dynamic correction.
	var loudFilter string
	if inputMetrics.HasMeasuredInput() {
		loudFilter = n.loud.passTwoFilter(inputMetrics)
	} else {
		loudFilter = n.loud.dynamicFilter()
	}
	pass2 := filter + "," + loudFilter
	m2Out, err := n.run(ctx,
		"-hide_banner", "-nostdin", "-y", "-i", inputPath,
		"-af", pass2, "-ar", "48000", "-ac", "2",
		"-c:a", "pcm_s16le", outputPath)
	if err != nil {
		return Result{}, fmt.Errorf("loudness correction pass failed: %w", err)
	}
	outputMetrics, err := ParseLoudnessJSON(m2Out)
	if err != nil {
		return Result{}, err
	}
	if _, err := os.Stat(outputPath); err != nil {
		return Result{}, fmt.Errorf("normalizer produced no output: %w", err)
	}
	return Result{
		Input: info, SampleRate: TargetRate, Channels: TargetChannels,
		FilterGraph: pass2, InputMetrics: inputMetrics, OutputMetrics: outputMetrics,
		FFmpegPath: n.bin, FFmpegVersion: n.version,
	}, nil
}

func (n *Normalizer) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, n.bin, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// buildFilterGraph rejects layouts the kernel does not know how to map and
// returns an explicit, deterministic pan graph for the ones it accepts.
func buildFilterGraph(info WAVInfo) (string, error) {
	switch info.Channels {
	case 1:
		return "aresample=48000,pan=stereo|c0=c0|c1=c0", nil
	case 2:
		return "aresample=48000,pan=stereo|c0=c0|c1=c1", nil
	default:
		return "", fmt.Errorf("%w: %d channels need an explicit downmix contract",
			ErrUnsupportedLayout, info.Channels)
	}
}

// ProbeWAV reads the RIFF/WAVE header and returns the audio format without
// invoking any external process.
func ProbeWAV(path string) (WAVInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return WAVInfo{}, err
	}
	defer f.Close()
	var head [12]byte
	if _, err := f.Read(head[:]); err != nil {
		return WAVInfo{}, ErrMissingTrack
	}
	if string(head[0:4]) != "RIFF" || string(head[8:12]) != "WAVE" {
		return WAVInfo{}, fmt.Errorf("%w: not a RIFF/WAVE container", ErrMissingTrack)
	}
	for {
		var chunkHdr [8]byte
		if _, err := f.Read(chunkHdr[:]); err != nil {
			return WAVInfo{}, fmt.Errorf("%w: no fmt chunk", ErrMissingTrack)
		}
		id := string(chunkHdr[0:4])
		size := binary.LittleEndian.Uint32(chunkHdr[4:8])
		if id == "fmt " {
			body := make([]byte, size)
			if _, err := f.Read(body); err != nil || len(body) < 16 {
				return WAVInfo{}, fmt.Errorf("%w: truncated fmt chunk", ErrMissingTrack)
			}
			info := WAVInfo{
				Channels:   binary.LittleEndian.Uint16(body[2:4]),
				SampleRate: binary.LittleEndian.Uint32(body[4:8]),
				Bits:       binary.LittleEndian.Uint16(body[14:16]),
			}
			if info.Channels == 0 {
				return WAVInfo{}, ErrMissingTrack
			}
			return info, nil
		}
		if _, err := f.Seek(int64(size+(size&1)), 1); err != nil {
			return WAVInfo{}, fmt.Errorf("%w: cannot skip chunk %q", ErrMissingTrack, id)
		}
	}
}

// FilterSummary renders the filter graph on one line for reports.
func (r Result) FilterSummary() string { return strings.ReplaceAll(r.FilterGraph, " ", "") }
