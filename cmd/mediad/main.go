// Command mediad is the acceptance entry point for the media-processing
// kernel. The "demo" subcommand generates deterministic MP4/MXF/RTP
// fixtures, runs them through metadata parsing, the integer clock, bounded
// fan-out, shared-clock fan-in and manifest verification, and prints the
// required summary fields with the documented exit codes.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"mediad/internal/clock"
	"mediad/internal/manifest"
	"mediad/internal/meta"
	"mediad/internal/samplegen"
)

// Exit codes required by the acceptance criteria.
const (
	exitOK       = 0
	exitInternal = 1
	exitIsolated = 2
)

type demoConfig struct {
	seed     int64
	duration time.Duration
	segment  time.Duration
	bitrates []int
	queueCap int
	faults   map[string]bool
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitInternal)
	}
	switch os.Args[1] {
	case "demo":
		cfg, err := parseDemo(os.Args[2:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "mediad: %v\n", err)
			os.Exit(exitInternal)
		}
		os.Exit(runDemo(cfg))
	case "-h", "--help", "help":
		usage()
		os.Exit(exitOK)
	default:
		fmt.Fprintf(os.Stderr, "mediad: unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(exitInternal)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: mediad demo --seed N --duration 12s --segment 2s
       --bitrates 450k,900k,1800k --queue 8
       [--faults truncate,slow,late-keyframe]

exit codes: 0 all invariants passed
            2 input corruption detected and isolated
            1 parameter or internal error
`)
}

func parseDemo(args []string) (*demoConfig, error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	var (
		seed     = fs.Int64("seed", 7, "deterministic seed")
		duration = fs.Duration("duration", 12*time.Second, "stream duration")
		segment  = fs.Duration("segment", 2*time.Second, "segment duration")
		br       = fs.String("bitrates", "450k,900k,1800k", "comma-separated target bitrates")
		queue    = fs.Int("queue", 8, "per-layer bounded queue capacity")
		faults   = fs.String("faults", "", "comma-separated: truncate,slow,late-keyframe")
	)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	rates, err := parseBitrates(*br)
	if err != nil {
		return nil, err
	}
	if len(rates) < 3 {
		return nil, fmt.Errorf("at least three bitrate tiers required, got %d", len(rates))
	}
	if *queue <= 0 {
		return nil, fmt.Errorf("queue capacity must be positive")
	}
	if *duration <= 0 || *segment <= 0 || *segment > *duration {
		return nil, fmt.Errorf("duration and segment must be positive, segment <= duration")
	}
	faultSet := map[string]bool{}
	for _, f := range strings.Split(*faults, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		switch f {
		case "truncate", "slow", "late-keyframe":
			faultSet[f] = true
		default:
			return nil, fmt.Errorf("unknown fault %q", f)
		}
	}
	return &demoConfig{
		seed: *seed, duration: *duration, segment: *segment,
		bitrates: rates, queueCap: *queue, faults: faultSet,
	}, nil
}

// parseBitrates accepts forms like "450k,900k,1800k" or raw bps integers.
func parseBitrates(s string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		mult := 1
		num := part
		if strings.HasSuffix(part, "k") || strings.HasSuffix(part, "K") {
			mult, num = 1000, part[:len(part)-1]
		} else if strings.HasSuffix(part, "m") || strings.HasSuffix(part, "M") {
			mult, num = 1_000_000, part[:len(part)-1]
		}
		v, err := strconv.Atoi(num)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("invalid bitrate %q", part)
		}
		out = append(out, v*mult)
	}
	return out, nil
}

// runDemo executes the full pipeline and returns the process exit code.
func runDemo(cfg *demoConfig) int {
	code, _, _, _ := execute(cfg)
	return code
}

// execute runs every pipeline stage and returns the exit code together with
// the built manifest and isolation/parse state so tests can assert the
// cross-module invariants without parsing stdout.
func execute(cfg *demoConfig) (int, *manifest.Manifest, bool, error) {
	const masterTS uint32 = 90000
	masterClock, err := clock.New(masterTS)
	if err != nil {
		return exitInternal, nil, false, err
	}
	segmentTicks := masterClock.SegmentTicks(cfg.segment)
	durationTicks := masterClock.Ticks(cfg.duration)

	isolated, perr := stageMetadata(cfg)

	inputs, recoveredLayers, err := stageStreamAndManifest(cfg, masterTS, segmentTicks, durationTicks)
	if err != nil {
		return exitInternal, nil, isolated, err
	}

	// Wait budget of 100 ms: keyframes arrive every 200 ms, so a withheld
	// boundary keyframe leaves no in-budget anchor and the segment is marked
	// missing, while healthy anchors stay within 10 ms of the ideal edge.
	builder, err := manifest.NewBuilder(masterTS, segmentTicks, segmentTicks/20, durationTicks)
	if err != nil {
		return exitInternal, nil, isolated, err
	}
	m, err := builder.Build(inputs)
	if err != nil {
		return exitInternal, nil, isolated, err
	}
	verr := manifest.Verify(m, len(cfg.bitrates))

	printSummary(cfg, m, recoveredLayers, isolated, perr, verr)

	// Exit 2 only when corruption was detected and isolated; verification
	// threshold breaches on an otherwise healthy run are internal errors.
	if isolated {
		return exitIsolated, m, true, perr
	}
	if verr != nil {
		fmt.Fprintf(os.Stderr, "mediad: invariant violation: %v\n", verr)
		return exitInternal, m, false, verr
	}
	return exitOK, m, false, perr
}

// stageMetadata generates and parses the container fixtures, reporting
// whether any corruption was isolated.
func stageMetadata(cfg *demoConfig) (bool, error) {
	mp4Spec := samplegen.MP4Spec{
		Tracks: []samplegen.MP4TrackSpec{{
			TrackID: 1, Timescale: 90000, Width: 1280, Height: 720,
			SampleN: 120, SampleDur: 3000, Rotation90: 1,
			ColourType: [4]byte{'n', 'c', 'l', 'x'},
			Primaries:  1, Transfer: 1, Matrix: 1, FullRange: true,
			CropX: 4, CropY: 4, CropW: 1272, CropH: 712,
		}},
		BadSampleRange: false,
		Truncate:       cfg.faults["truncate"],
	}
	mp4Data, err := samplegen.BuildMP4(mp4Spec)
	if err != nil {
		return false, err
	}
	mp4Descs, mp4Err := meta.ParseMP4(bytesReader(mp4Data))

	mxfSpec := samplegen.MXFSpec{
		Tracks: []samplegen.MXFTrackSpec{{
			TrackID: 2, EditRateN: 25, EditRateD: 1, Width: 1280, Height: 720,
			Rotation90: 2, Primaries: 9, Transfer: 1, Matrix: 9,
		}},
		Truncate: false,
	}
	mxfDescs, mxfErr := meta.ParseMXF(samplegen.BuildMXF(mxfSpec))

	resolved, err := meta.Resolve(append(mp4Descs, mxfDescs...))
	if err != nil {
		return false, err
	}
	isolated := false
	for _, dec := range resolved.Decisions {
		fmt.Fprintf(os.Stderr, "meta: %s\n", dec)
	}
	if mp4Err != nil {
		var ce *clock.Error
		if errors.As(mp4Err, &ce) && ce.Recoverable {
			isolated = true
			fmt.Fprintf(os.Stderr, "meta: isolated MP4 corruption: %v\n", mp4Err)
		}
	}
	if mxfErr != nil {
		var ce *clock.Error
		if errors.As(mxfErr, &ce) && ce.Recoverable {
			isolated = true
			fmt.Fprintf(os.Stderr, "meta: isolated MXF corruption: %v\n", mxfErr)
		}
	}
	return isolated, errors.Join(mp4Err, mxfErr)
}

func failInternal(err error) int {
	fmt.Fprintf(os.Stderr, "mediad: internal error: %v\n", err)
	return exitInternal
}
