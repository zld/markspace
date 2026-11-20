package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"loudscan/internal/ebu"
)

// Limits bounds the resources a single input may consume. Zero means the
// default, a negative value disables the check.
type Limits struct {
	MaxBytes   int64
	MaxSeconds float64
}

// Default limits chosen to keep a batch bounded while comfortably covering
// game-audio deliverables (a 24-bit 96 kHz 7.1 file runs about 2 GB/hour).
const (
	DefaultMaxBytes   = int64(2 << 30)
	DefaultMaxSeconds = 3600.0
)

// Info describes the selected audio stream.
type Info struct {
	SampleRate int
	Channels   int
	// Layout is the validated BS.1770 channel layout.
	Layout ebu.Layout
	// RawLayout is the layout string ffprobe reported (may be empty).
	RawLayout string
	// Declared is true when the caller supplied the layout explicitly.
	Declared bool
	Duration float64
	Codec    string
	BitDepth int
}

// ProbeError records which stage failed while inspecting a file. The batch
// layer preserves it so the report can name the failing stage.
type ProbeError struct {
	Stage  string
	Reason string
	Err    error
}

func (e *ProbeError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Stage, e.Reason, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Stage, e.Reason)
}

func (e *ProbeError) Unwrap() error { return e.Err }

type ffprobeStream struct {
	CodecType     string `json:"codec_type"`
	CodecName     string `json:"codec_name"`
	SampleRate    string `json:"sample_rate"`
	Channels      int    `json:"channels"`
	ChannelLayout string `json:"channel_layout"`
	BitsPerSample int    `json:"bits_per_sample"`
	Duration      string `json:"duration"`
}

type ffprobeOutput struct {
	Streams []ffprobeStream `json:"streams"`
	Format  struct {
		Duration string `json:"duration"`
		Size     string `json:"size"`
	} `json:"format"`
}

// Options configures a probe.
type Options struct {
	FFprobePath string
	Limits      Limits
	// LayoutOverride is a comma or dash separated channel list; empty means
	// only standard layouts are accepted.
	LayoutOverride string
}

// Probe inspects a file with ffprobe, validates the audio stream and resolves
// the channel layout.
func Probe(ctx context.Context, path string, opts Options) (Info, error) {
	if opts.FFprobePath == "" {
		opts.FFprobePath = "ffprobe"
	}
	limits := opts.Limits.withDefaults()

	var override []string
	if opts.LayoutOverride != "" {
		var err error
		override, err = SplitLayout(opts.LayoutOverride)
		if err != nil {
			return Info{}, &ProbeError{Stage: "probe", Reason: "invalid layout override", Err: err}
		}
		if _, err := ebu.NewLayout(override); err != nil {
			return Info{}, &ProbeError{Stage: "probe", Reason: "invalid layout override", Err: err}
		}
	}

	cmd := exec.CommandContext(ctx, opts.FFprobePath,
		"-v", "error",
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		"--", path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return Info{}, &ProbeError{Stage: "probe", Reason: "ffprobe timed out", Err: ctx.Err()}
		}
		return Info{}, &ProbeError{Stage: "probe", Reason: "ffprobe failed", Err: fmt.Errorf("%v: %s", err, tail(stderr.String()))}
	}

	var out ffprobeOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return Info{}, &ProbeError{Stage: "probe", Reason: "unreadable ffprobe output", Err: err}
	}
	return validate(path, out, override, limits)
}

func validate(path string, out ffprobeOutput, override []string, limits Limits) (Info, error) {
	var audio *ffprobeStream
	for i := range out.Streams {
		if out.Streams[i].CodecType == "audio" {
			audio = &out.Streams[i]
			break
		}
	}
	if audio == nil {
		return Info{}, &ProbeError{Stage: "probe", Reason: "no audio stream"}
	}
	if audio.Channels <= 0 {
		return Info{}, &ProbeError{Stage: "probe", Reason: "audio stream reports no channels"}
	}
	rate, err := strconv.Atoi(audio.SampleRate)
	if err != nil || rate <= 0 {
		return Info{}, &ProbeError{Stage: "probe", Reason: fmt.Sprintf("invalid sample rate %q", audio.SampleRate)}
	}
	duration := parseFloat(audio.Duration)
	if duration == 0 {
		duration = parseFloat(out.Format.Duration)
	}
	if limits.MaxBytes >= 0 {
		if size := parseFloat(out.Format.Size); size > 0 && int64(size) > limits.MaxBytes {
			return Info{}, &ProbeError{Stage: "probe", Reason: fmt.Sprintf("file is %.1f MiB, above the %.1f MiB limit", size/(1<<20), float64(limits.MaxBytes)/(1<<20))}
		}
	}
	if limits.MaxSeconds >= 0 && duration > limits.MaxSeconds {
		return Info{}, &ProbeError{Stage: "probe", Reason: fmt.Sprintf("duration %.1f s exceeds the %.1f s limit", duration, limits.MaxSeconds)}
	}

	labels := override
	declared := len(override) > 0
	if !declared {
		name := audio.ChannelLayout
		if name == "" {
			name = defaultLayoutName(audio.Channels)
		}
		labels = ebu.StandardLayouts[name]
		if labels == nil {
			return Info{}, &ProbeError{Stage: "probe", Reason: fmt.Sprintf("unsupported channel layout %q (%d channels); pass --layout to declare it", name, audio.Channels)}
		}
	}
	if len(labels) != audio.Channels {
		return Info{}, &ProbeError{Stage: "probe", Reason: fmt.Sprintf("layout declares %d channels but the stream has %d", len(labels), audio.Channels)}
	}
	layout, err := ebu.NewLayout(labels)
	if err != nil {
		return Info{}, &ProbeError{Stage: "probe", Reason: "invalid channel layout", Err: err}
	}
	if _, err := ebu.KWeightingFor(rate); err != nil {
		return Info{}, &ProbeError{Stage: "probe", Reason: "unsupported sample rate", Err: err}
	}
	return Info{
		SampleRate: rate,
		Channels:   audio.Channels,
		Layout:     layout,
		RawLayout:  audio.ChannelLayout,
		Declared:   declared,
		Duration:   duration,
		Codec:      audio.CodecName,
		BitDepth:   audio.BitsPerSample,
	}, nil
}

func defaultLayoutName(channels int) string {
	if channels == 1 {
		return "mono"
	}
	if channels == 2 {
		return "stereo"
	}
	return ""
}

// SplitLayout parses a channel list separated by commas, dashes or spaces.
func SplitLayout(s string) ([]string, error) {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '-' || r == '+' || r == ' ' || r == '\t'
	})
	if len(fields) == 0 {
		return nil, errors.New("layout contains no channel labels")
	}
	return fields, nil
}

func (l Limits) withDefaults() Limits {
	if l.MaxBytes == 0 {
		l.MaxBytes = DefaultMaxBytes
	}
	if l.MaxSeconds == 0 {
		l.MaxSeconds = DefaultMaxSeconds
	}
	return l
}

func parseFloat(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

// tail returns the last line of a command's stderr, trimmed and length
// limited, so failures stay informative without dumping whole logs.
func tail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "no diagnostics"
	}
	lines := strings.Split(s, "\n")
	return truncateRunes(strings.TrimSpace(lines[len(lines)-1]), 240)
}

// truncateRunes shortens s to at most n bytes without splitting a UTF-8 rune,
// so a report line stays valid text even when FFmpeg echoes a non-ASCII path.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "..."
}
