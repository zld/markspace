// Package pipeline runs the end-to-end conversion:
// RTP/RTCP parsing -> clock mapping -> watermark/replay -> audio processing
// -> MP4 muxing and manifest -> quality statistics.
package pipeline

import (
	"os"
	"path/filepath"
	"strings"

	"mediaforge/internal/audio"
	"mediaforge/internal/mediaerr"
	"mediaforge/internal/packet"
	"mediaforge/internal/quality"
)

// Options configures a run.
type Options struct {
	InputPath  string
	OutDir     string
	SampleRate int64
	Channels   int
	TargetLUFS float64
	// ClockRate is the RTP timestamp rate of the source stream. It defaults to
	// SampleRate but can differ (e.g. 90000 for an AES67 stream).
	ClockRate int64
	// DelayMs is the reorder buffer that advances the watermark.
	DelayMs int
	// PayloadLayout is mono/stereo channel count carried by each RTP payload.
	// It is independent of the output Channels so a mono source can be
	// delivered as stereo.
	PayloadLayout int
	// BitsPerSample is the source PCM width; only 16 is currently accepted.
	BitsPerSample int
	// CeilingDBFS is the limiter ceiling.
	CeilingDBFS float64
	// TargetBps is optional; when set the bitrate deviation is verified.
	TargetBps float64
}

// Defaults fills unset options and validates ranges.
func (o *Options) Defaults() error {
	if o.SampleRate == 0 {
		o.SampleRate = 48000
	}
	if o.Channels == 0 {
		o.Channels = 2
	}
	if o.ClockRate == 0 {
		o.ClockRate = o.SampleRate
	}
	if o.PayloadLayout == 0 {
		o.PayloadLayout = o.Channels
	}
	if o.BitsPerSample == 0 {
		o.BitsPerSample = 16
	}
	if o.DelayMs == 0 {
		o.DelayMs = 40
	}
	if o.CeilingDBFS == 0 {
		o.CeilingDBFS = -1
	}
	if o.TargetLUFS == 0 {
		o.TargetLUFS = -23
	}
	switch {
	case !audio.IsCommonRate(o.SampleRate) || !audio.IsCommonRate(o.ClockRate):
		return mediaerr.New(mediaerr.CodeParam, "unsupported sample/clock rate %d/%d", o.SampleRate, o.ClockRate)
	case o.Channels != 1 && o.Channels != 2:
		return mediaerr.New(mediaerr.CodeParam, "output channels must be 1 or 2")
	case o.PayloadLayout != 1 && o.PayloadLayout != 2:
		return mediaerr.New(mediaerr.CodeParam, "payload layout must be mono or stereo")
	case o.BitsPerSample != 16:
		return mediaerr.New(mediaerr.CodeParam, "only 16-bit PCM payloads are supported")
	case o.DelayMs < 0 || o.DelayMs > 2000:
		return mediaerr.New(mediaerr.CodeParam, "delay must be 0..2000 ms")
	case o.CeilingDBFS >= 0:
		return mediaerr.New(mediaerr.CodeParam, "ceiling must be below 0 dBFS")
	case o.OutDir == "":
		return mediaerr.New(mediaerr.CodeParam, "output directory is required")
	}
	return nil
}

// Result is the complete output of a run.
type Result struct {
	Stats        quality.Stats
	MP4Path      string
	ManifestPath string
	StatsPath    string
}

// BaseName is the shared file stem derived from the input name.
func BaseName(path string) string {
	base := filepath.Base(path)
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	return base
}

// Run processes one capture file and writes <stem>.mp4,
// <stem>.manifest.json and <stem>.stats.json into OutDir.
func Run(opts Options) (Result, error) {
	if err := opts.Defaults(); err != nil {
		return Result{}, err
	}
	opts.InputPath = filepath.Clean(opts.InputPath)
	st, err := os.Stat(opts.InputPath)
	if err != nil {
		return Result{}, mediaerr.New(mediaerr.CodeIO, "stat input: %v", err)
	}
	if st.IsDir() {
		return Result{}, mediaerr.New(mediaerr.CodeParam, "input is a directory")
	}
	if st.Size() > packet.MaxInputSize {
		return Result{}, mediaerr.New(mediaerr.CodeParam, "input exceeds 2 GiB")
	}
	f, err := os.Open(opts.InputPath)
	if err != nil {
		return Result{}, mediaerr.New(mediaerr.CodeIO, "open input: %v", err)
	}
	defer f.Close()
	stream, err := packet.ReadStream(f, packet.MaxInputSize)
	if err != nil {
		return Result{}, mediaerr.New(mediaerr.CodeTruncated, "capture: %v", err)
	}
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return Result{}, mediaerr.New(mediaerr.CodeIO, "create output dir: %v", err)
	}
	res := Result{
		MP4Path:      filepath.Join(opts.OutDir, BaseName(opts.InputPath)+".mp4"),
		ManifestPath: filepath.Join(opts.OutDir, BaseName(opts.InputPath)+".manifest.json"),
		StatsPath:    filepath.Join(opts.OutDir, BaseName(opts.InputPath)+".stats.json"),
	}
	conv := newConverter(opts, stream)
	stats, err := conv.finish(res, quality.Stats{InputBytes: stream.Bytes})
	if err != nil {
		return Result{}, err
	}
	res.Stats = stats
	return res, nil
}
