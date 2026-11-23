// Command mediacore drives the processing kernel over seeded synthetic CMAF
// input and prints the checks needed for acceptance. It is a thin shell over
// internal/pipeline; no service or network access is involved.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"mediacore/internal/fault"
	"mediacore/internal/pipeline"
	"mediacore/internal/verify"
)

const (
	exitOK            = 0
	exitUnrecoverable = 1
	exitDamaged       = 2
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitUnrecoverable
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "help", "-h", "--help":
		usage()
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage()
		return exitUnrecoverable
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `mediacore - deterministic CMAF/fMP4 timeline kernel

  mediacore demo   --seed N --tracks N --fragments N --inject a,b,c \
                   --checkpoint PATH --resume --frames PATH --manifest PATH
  mediacore verify --manifest PATH --frames PATH

Exit codes: 0 complete, 2 damaged input processed with recorded faults,
1 unrecoverable or verification failure.
`)
}

type flags struct {
	seed, tracks, fragments int
	inject, checkpoint      string
	resume                  bool
	frames, manifest        string
	width, height           int
	timescale               int
	verbose                 bool
}

func parseFlags(args []string) (flags, error) {
	var f flags
	fs := flag.NewFlagSet("mediacore", flag.ContinueOnError)
	fs.IntVar(&f.seed, "seed", 41, "deterministic seed")
	fs.IntVar(&f.tracks, "tracks", 2, "track count (at least 2 for a merged timeline)")
	fs.IntVar(&f.fragments, "fragments", 24, "fragment count")
	fs.StringVar(&f.inject, "inject", "", "comma separated fault kinds")
	fs.StringVar(&f.checkpoint, "checkpoint", "", "checkpoint file path")
	fs.BoolVar(&f.resume, "resume", false, "resume from a matching checkpoint")
	fs.StringVar(&f.frames, "frames", "tmp/frames.jsonl", "frame sequence output")
	fs.StringVar(&f.manifest, "manifest", "tmp/manifest.json", "manifest output")
	fs.IntVar(&f.width, "width", 64, "review frame width")
	fs.IntVar(&f.height, "height", 36, "review frame height")
	fs.IntVar(&f.timescale, "timescale", 90000, "timeline timescale")
	fs.BoolVar(&f.verbose, "v", false, "print per-fault detail")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	return f, nil
}

func runDemo(args []string) int {
	f, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitUnrecoverable
	}
	cfg := pipeline.Config{
		Seed:           int64(f.seed),
		Tracks:         f.tracks,
		Fragments:      f.fragments,
		Faults:         splitList(f.inject),
		CheckpointPath: f.checkpoint,
		Resume:         f.resume,
		FrameWidth:     f.width,
		FrameHeight:    f.height,
		Timescale:      uint32(f.timescale),
		FramesOutput:   f.frames,
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "invalid configuration:", err)
		return exitUnrecoverable
	}
	injected, inputs, err := pipeline.Generate(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate:", err)
		return exitUnrecoverable
	}
	result, err := pipeline.Run(cfg, injected, inputs)
	if err != nil {
		// Unrecoverable: resume refused or a stage could not complete.
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitUnrecoverable
	}
	if err := result.WriteOutputs(f.frames, f.manifest); err != nil {
		fmt.Fprintln(os.Stderr, "write outputs:", err)
		return exitUnrecoverable
	}
	printDemo(result, f.verbose)
	return exitCode(result)
}

// exitCode maps a completed run to the documented exit status.
func exitCode(r *pipeline.Result) int {
	if len(r.Failures) > 0 {
		return exitUnrecoverable
	}
	if r.Faults > 0 || len(r.Errors) > 0 {
		return exitDamaged
	}
	return exitOK
}

func printDemo(r *pipeline.Result, verbose bool) {
	fmt.Printf("fragments=%d\n", r.FragmentTotal)
	fmt.Printf("fragments_seen=%d\n", r.FragmentsSeen)
	fmt.Printf("fragments_reused=%d\n", r.ReusedFragments)
	fmt.Printf("tracks=%d\n", r.TrackCount)
	fmt.Printf("faults=%d\n", r.Faults)
	fmt.Printf("first_error_offset=%d\n", r.FirstErrorOffset)
	fmt.Printf("first_error_code=%s\n", r.FirstErrorCode)
	fmt.Printf("offset_error_bytes=%d\n", OffsetErrorBytes(r))
	fmt.Printf("faults_reached=%d\n", FaultsReached(r))
	fmt.Printf("recovered=%v\n", r.Recovered)
	fmt.Printf("resumed=%v\n", r.Resumed)
	fmt.Printf("resume_ms=%.3f\n", r.ResumeMS)
	fmt.Printf("elapsed_ms=%.3f\n", r.ElapsedMS)
	fmt.Printf("frames=%d\n", r.FrameCount)
	fmt.Printf("sha256=%s\n", r.Frames.Digest)
	fmt.Printf("manifest_sha256=%s\n", r.Manifest.SHA256Digest())
	fmt.Printf("invariant_failures=%d\n", len(r.Failures))
	fmt.Printf("gaps=%d\n", len(r.Gaps))
	if r.Repair != nil {
		fmt.Printf("repair_duplicates=%d\n", r.Repair.Duplicates)
		fmt.Printf("repair_regressions=%d\n", r.Repair.Regressions)
		fmt.Printf("repair_sequence_gaps=%d\n", r.Repair.SequenceGaps)
	}
	if verbose {
		for _, e := range r.Errors {
			fmt.Printf("fault code=%s file=%s fragment=%d box=%s offset=%d\n", e.Code, e.File, e.FragmentSeq, e.BoxType, e.Offset)
		}
		for _, e := range r.Repair.Discontinuities {
			fmt.Printf("repair kind=%s offset=%d track=%d fragment=%d repaired=%v\n", e.Kind, e.Offset, e.TrackID, e.FragmentIndex, e.Repaired)
		}
	}
	for _, fail := range r.Failures {
		fmt.Printf("invariant_failure=%s\n", fail)
	}
}

// OffsetErrorBytes is the distance between the reported first error and the
// nearest byte that was actually damaged. Zero means the first reported error
// landed exactly on an injected fault position.
//
// Several faults can be combined in one input, and a structural defect stops
// parsing at its own byte: a fault placed after that point is unreachable by
// design, not misreported. FaultsReached reports how many injections were
// matched exactly, so an unreachable tail is visible rather than hidden.
func OffsetErrorBytes(r *pipeline.Result) int64 {
	if len(r.InjectedOffsets) == 0 || len(r.Errors) == 0 {
		return 0
	}
	return nearestDistance(r.FirstErrorOffset, r.InjectedOffsets)
}

// FaultsReached counts injections that at least one reported error matched
// byte-exactly.
func FaultsReached(r *pipeline.Result) int {
	n := 0
	for _, injected := range r.InjectedOffsets {
		if nearestDistance(injected, offsetsOf(r.Errors)) == 0 {
			n++
		}
	}
	return n
}

func offsetsOf(errs []*fault.Error) []int64 {
	out := make([]int64, len(errs))
	for i, e := range errs {
		out[i] = e.Offset
	}
	return out
}

func nearestDistance(off int64, targets []int64) int64 {
	best := int64(-1)
	for _, t := range targets {
		d := off - t
		if d < 0 {
			d = -d
		}
		if best < 0 || d < best {
			best = d
		}
	}
	if best < 0 {
		return 0
	}
	return best
}

func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	manifestPath := fs.String("manifest", "tmp/manifest.json", "manifest path")
	framesPath := fs.String("frames", "tmp/frames.jsonl", "frame sequence path")
	verbose := fs.Bool("v", false, "print per-failure detail")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitUnrecoverable
	}
	report, err := verify.Files(verify.Options{ManifestPath: *manifestPath, FramesPath: *framesPath})
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify:", err)
		fmt.Printf("invariant_failures=1\n")
		return exitUnrecoverable
	}
	fmt.Printf("invariant_failures=%d\n", len(report.Failures))
	fmt.Printf("manifest_sha256=%s\n", report.ManifestSHA256)
	fmt.Printf("frame_count=%d\n", report.FrameCount)
	fmt.Printf("frame_digest=%s\n", report.FrameDigest)
	if *verbose {
		for _, f := range report.Failures {
			fmt.Printf("invariant_failure=%s\n", f)
		}
	}
	if len(report.Failures) > 0 {
		return exitUnrecoverable
	}
	return exitOK
}

func splitList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	return strings.Split(raw, ",")
}
