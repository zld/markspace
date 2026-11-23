package main

import (
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	"mediadist/demux"
	"mediadist/pipeline"
	"mediadist/segment"
	"mediadist/timeline"
)

// demoArgs holds parsed `demo` flags.
type demoArgs struct {
	seed          int64
	duration      time.Duration
	bitrates      []uint64
	out           string
	faultCase     demux.FaultCase
	colorPriority string
}

func parseDemoFlags(args []string) (demoArgs, error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	var (
		seed      = fs.Int64("seed", 1, "deterministic fixture seed")
		duration  = fs.String("duration", "12s", "clip duration (Go duration, e.g. 12s)")
		bitrates  = fs.String("bitrates", "800k,1600k,3000k", "comma-separated target bitrates")
		out       = fs.String("out", "", "output directory (required)")
		faultCase = fs.String("case", "", "fault injection case")
		colorPrio = fs.String("color-priority", "container", "color source priority")
	)
	if err := fs.Parse(args); err != nil {
		return demoArgs{}, err
	}
	if *out == "" {
		return demoArgs{}, errors.New("--out is required")
	}
	d, err := time.ParseDuration(*duration)
	if err != nil {
		return demoArgs{}, fmt.Errorf("bad --duration: %w", err)
	}
	if d <= 0 {
		return demoArgs{}, errors.New("--duration must be positive")
	}
	brs, err := parseBitrates(*bitrates)
	if err != nil {
		return demoArgs{}, err
	}
	fault := demux.FaultCase(*faultCase)
	if fault != demux.FaultNone && !demux.ValidFault(fault) {
		return demoArgs{}, fmt.Errorf("unknown --case %q", *faultCase)
	}
	return demoArgs{
		seed: *seed, duration: d, bitrates: brs, out: *out,
		faultCase: fault, colorPriority: *colorPrio,
	}, nil
}

// parseBitrates accepts forms like "800k", "1600k", "3m", or raw bps.
func parseBitrates(s string) ([]uint64, error) {
	parts := strings.Split(s, ",")
	out := make([]uint64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		mult := uint64(1)
		switch p[len(p)-1] {
		case 'k', 'K':
			mult, p = 1000, p[:len(p)-1]
		case 'm', 'M':
			mult, p = 1_000_000, p[:len(p)-1]
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad bitrate %q: %w", p, err)
		}
		out = append(out, n*mult)
	}
	if len(out) == 0 {
		return nil, errors.New("at least one bitrate is required")
	}
	return out, nil
}

// errorCode extracts a stable code from a pipeline RunError or LocError chain.
func errorCode(err error) string {
	if err == nil {
		return ""
	}
	var re *pipeline.RunError
	if errors.As(err, &re) {
		return re.Code
	}
	var le *timeline.LocError
	if errors.As(err, &le) {
		return le.Code
	}
	return "INTERNAL"
}

// buildVideoSpec constructs the 23.976 fps video spec with bitrate-calibrated
// frame sizes and the color metadata for the requested fault case.
func buildVideoSpec(seed int64, seconds int, targetBps uint64, fault demux.FaultCase) demux.VideoSpec {
	frameCount := uint32(24 * seconds)
	vs := demux.VideoSpec{
		Timescale:      24000,
		Width:          1280,
		Height:         720,
		KeyframeEvery:  48,
		FrameDelta:     1001,
		FrameCount:     frameCount,
		FrameSizeBytes: demux.VideoFrameSizeForTotalBitrate(targetBps, 24000, 1001),
		Color:          &demux.ColorValue{Primaries: 9, Transfer: 16, Matrix: 9},
	}
	if fault == demux.FaultColorConflict {
		vs.BitstreamColor = &demux.ColorValue{Primaries: 1, Transfer: 1, Matrix: 1}
	}
	return vs
}

// buildAudioSpec constructs the 48 kHz audio spec (~12 s).
func buildAudioSpec(seconds int, fault demux.FaultCase) demux.AudioSpec {
	channels := uint8(timeline.LayoutStereo)
	if fault == demux.FaultBadLayout {
		channels = 255
	}
	return demux.AudioSpec{
		Timescale:    48000,
		SampleRate:   48000,
		Channels:     channels,
		FrameSamples: 1024,
		FrameCount:   uint32(seconds) * 47,
	}
}

// colorPriorityToSources maps the CLI flag to an ordered source list.
func colorPriorityToSources(s string) []timeline.ColorSource {
	switch s {
	case "bitstream":
		return []timeline.ColorSource{timeline.SourceBitstream, timeline.SourceContainer}
	case "cli":
		return []timeline.ColorSource{timeline.SourceCLI, timeline.SourceBitstream, timeline.SourceContainer}
	default:
		return []timeline.ColorSource{timeline.SourceContainer, timeline.SourceBitstream}
	}
}

var _ = segment.DefaultConfig
