// medfix is the local verification entry point for the media timeline
// repair library. It generates self-contained samples, runs the four-stage
// pipeline and prints acceptance metrics as stable key=value lines.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"medfix/internal/fixture"
	"medfix/internal/journal"
	"medfix/internal/pipeline"
	"medfix/internal/timeline"
)

const (
	exitOK       = 0
	exitMetrics  = 1 // acceptance thresholds or invariants failed
	exitUsage    = 2 // bad arguments or input format
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitUsage)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	case "verify":
		os.Exit(runVerify(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
		os.Exit(exitOK)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(exitUsage)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: medfix demo --seed N [--interrupt-after stage] [--workdir DIR]")
	fmt.Fprintln(os.Stderr, "       medfix verify --seed N [--repeat N] [--workdir DIR]")
}

type flags struct {
	seed           int64
	repeat         int
	interruptStage string
	workdir        string
}

func parseFlags(args []string) (flags, error) {
	f := flags{seed: 1, repeat: 1, workdir: filepath.Join(os.TempDir(), "medfix-demo")}
	for i := 0; i < len(args); i++ {
		a := args[i]
		need := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag %s needs a value", a)
			}
			i++
			return args[i], nil
		}
		switch {
		case a == "--seed":
			v, err := need()
			if err != nil {
				return f, err
			}
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return f, fmt.Errorf("invalid --seed: %w", err)
			}
			f.seed = n
		case a == "--repeat":
			v, err := need()
			if err != nil {
				return f, err
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return f, fmt.Errorf("invalid --repeat: %s", v)
			}
			f.repeat = n
		case a == "--interrupt-after":
			v, err := need()
			if err != nil {
				return f, err
			}
			if v != journal.StageParse && v != journal.StageTransform &&
				v != journal.StageValidate && v != journal.StageCommit {
				return f, fmt.Errorf("invalid --interrupt-after stage %q", v)
			}
			f.interruptStage = v
		case a == "--workdir":
			v, err := need()
			if err != nil {
				return f, err
			}
			f.workdir = v
		default:
			return f, fmt.Errorf("unknown flag %q", a)
		}
	}
	return f, nil
}

// buildInputs generates the normal, truncated, ambiguous-rollover and
// interrupted-recovery sample set entirely in process.
func buildInputs(seed int64) []pipeline.Input {
	mp4 := fixture.GenerateMP4(seed, []fixture.MP4TrackSpec{
		{TrackID: 1, Timescale: 90000, Samples: 200, Delta: 3000, KeyEvery: 1, PayloadBase: 10},
		{TrackID: 2, Timescale: 48000, Samples: 8, Delta: 1024, KeyEvery: 1, PayloadBase: 40},
	})
	rtp := fixture.GenerateRTP(seed, fixture.RTPSpec{
		ClockRate: 90000, Packets: 24, StartSeq: 0, StartTS: 0,
		TSIncrement: 3000, ID3Every: 6,
	})
	return []pipeline.Input{
		{ID: "mp4-normal", Kind: "mp4", Data: mp4.Bytes},
		{ID: "rtp-normal", Kind: "rtp", Data: rtp.Bytes, ClockRate: 90000},
	}
}

func printReport(rep *pipeline.Report) {
	fmt.Printf("roundtrip_match_rate=%.4f\n", rep.RoundtripMatchRate)
	fmt.Printf("keyframe_hit_rate=%.4f\n", rep.KeyframeHitRate)
	fmt.Printf("segments=%d\n", rep.Segments)
	fmt.Printf("resumed_stage=%s\n", blankAsDash(rep.ResumedStage))
	fmt.Printf("invariant_failures=%d\n", rep.InvariantFailures)
	fmt.Printf("index_sha256=%s\n", rep.IndexSHA256)
	for i, h := range rep.SegmentSHA256 {
		fmt.Printf("segment_sha256[%d]=%s\n", i, h)
	}
	for _, off := range rep.SourceOffsets {
		fmt.Printf("source_offset=%d\n", off)
	}
	if len(rep.NoKeyframeTracks) > 0 {
		fmt.Printf("no_keyframe_tracks=%s\n", strings.Join(rep.NoKeyframeTracks, ","))
	}
}

func blankAsDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func runDemo(args []string) int {
	f, err := parseFlags(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "medfix demo: %v\n", err)
		return exitUsage
	}
	dir := filepath.Join(f.workdir, "demo-"+strconv.FormatInt(f.seed, 10))
	_ = os.RemoveAll(dir)

	// 1. Normal run, optionally interrupted after a chosen stage.
	fmt.Println("sample=normal")
	rep, err := execute(dir+"-normal", f.seed, pipeline.Options{
		Config: timeline.DefaultConfig(), Interrupt: f.interruptStage,
	})
	if f.interruptStage != "" {
		if !errors.Is(err, pipeline.ErrInterrupted) {
			fmt.Fprintf(os.Stderr, "expected interruption, got %v\n", err)
			return exitMetrics
		}
		fmt.Printf("interrupted_after=%s\n", f.interruptStage)
		// Resume from the most recent valid checkpoint.
		rep, err = execute(dir+"-normal", f.seed, pipeline.Options{
			Config: timeline.DefaultConfig(),
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "resume failed: %v\n", err)
			return exitMetrics
		}
		fmt.Printf("resumed_stage=%s\n", blankAsDash(rep.ResumedStage))
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "pipeline failed: %v\n", err)
		return exitMetrics
	}
	printReport(rep)

	// 2. Corrupt samples report concrete offsets instead of crashing.
	reportCorruptSamples(f.seed)

	if !acceptanceOK(rep) {
		return exitMetrics
	}
	return exitOK
}

func execute(dir string, seed int64, opts pipeline.Options) (*pipeline.Report, error) {
	opts.WorkDir = dir
	r, err := pipeline.New("medfix-run", buildInputs(seed), opts)
	if err != nil {
		return nil, err
	}
	return r.Execute()
}


func acceptanceOK(rep *pipeline.Report) bool {
	return rep.RoundtripMatchRate == 1.0 &&
		rep.KeyframeHitRate > 0.99 &&
		rep.InvariantFailures == 0 &&
		len(rep.Conflicts) == 0
}

func runVerify(args []string) int {
	f, err := parseFlags(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "medfix verify: %v\n", err)
		return exitUsage
	}
	dir := filepath.Join(f.workdir, "verify-"+strconv.FormatInt(f.seed, 10))
	_ = os.RemoveAll(dir)

	var prev []string
	allStable := true
	for i := 0; i < f.repeat; i++ {
		rep, err := execute(filepath.Join(dir, "run"), f.seed,
			pipeline.Options{Config: timeline.DefaultConfig()})
		if err != nil {
			fmt.Fprintf(os.Stderr, "repeat %d failed: %v\n", i, err)
			return exitMetrics
		}
		if i > 0 && !equalStrings(prev, rep.SegmentSHA256) {
			allStable = false
		}
		prev = rep.SegmentSHA256
		if !acceptanceOK(rep) {
			printReport(rep)
			return exitMetrics
		}
	}
	stableCount := 0
	if allStable {
		stableCount = len(prev)
	}
	// Final report from the last execution for completeness.
	rep, _ := execute(filepath.Join(dir, "run"), f.seed,
		pipeline.Options{Config: timeline.DefaultConfig()})
	printReport(rep)
	fmt.Printf("repeat=%d stable_segment_hashes=%d\n", f.repeat, stableCount)
	if stableCount != rep.Segments {
		return exitMetrics
	}
	return exitOK
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
