// Command medpipe is the acceptance entry point for the media pipeline. It
// operates entirely on local files and has two subcommands:
//
//	medpipe demo --generate --out DIR
//	    generate the normal, truncated, timestamp-rollback, color-conflict
//	    and unrepairable fixture set.
//	medpipe run --input DIR --target-bitrate N --resume
//	    scan, index, validate and stream every track in DIR/tracks.json,
//	    print the headline metrics and write manifest.json plus MP4 outputs.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	medpipe "github.com/zld/medpipe/internal"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "demo":
		err = runDemo(os.Args[2:])
	case "run":
		_, err = runPipeline(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "medpipe: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `medpipe — local elementary-stream indexing and streaming pipeline

usage:
  medpipe demo --generate --out DIR
  medpipe run --input DIR [--target-bitrate BPS] [--resume] [--work-dir DIR]
`)
}

func runDemo(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	generate := fs.Bool("generate", false, "generate the fixture set")
	out := fs.String("out", "", "output directory for generated fixtures")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*generate {
		return fmt.Errorf("demo requires --generate")
	}
	if *out == "" {
		return fmt.Errorf("demo requires --out DIR")
	}
	main, fatal, err := medpipe.GenerateFixtures(*out, medpipe.DefaultFixtureParams())
	if err != nil {
		return err
	}
	fmt.Printf("fixtures generated under %s\n", *out)
	fmt.Printf("  main batch tracks : %d\n", len(main))
	fmt.Printf("  fatal batch tracks: %d (under %s)\n", len(fatal),
		filepath.Join(medpipe.DefaultFixtureNames().FatalDir, medpipe.DefaultFixtureNames().FatalSpec))
	return nil
}

func runPipeline(args []string) (*medpipe.Manifest, error) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	input := fs.String("input", "", "directory containing tracks.json and capture files")
	target := fs.Float64("target-bitrate", 4_000_000, "target aggregate bitrate in bps")
	resume := fs.Bool("resume", false, "resume interrupted tracks from their last keyframe")
	workDir := fs.String("work-dir", "", "directory for indexes/fragments (default: INPUT/.medpipe)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if *input == "" {
		return nil, fmt.Errorf("run requires --input DIR")
	}
	work := *workDir
	if work == "" {
		work = filepath.Join(*input, ".medpipe")
	}
	specs, err := medpipe.LoadSpecs(*input)
	if err != nil {
		return nil, fmt.Errorf("load track specs: %w", err)
	}
	opts := medpipe.PipelineOptions{
		InputDir: *input, WorkDir: work, ChunkSize: 12,
		TargetBPS: *target, Resume: *resume,
	}
	m, err := medpipe.RunBatch(specs, opts)
	if err != nil {
		return m, err
	}
	printTracks(m)
	printMetrics(m)
	printErrors(m)
	// The fatal fixture batch is a built-in negative control: it must fail
	// with a track and byte offset, proving the non-zero-exit path.
	if ferr := verifyFatalBatch(*input, opts); ferr != nil {
		fmt.Fprintf(os.Stderr, "fatal-control: %v\n", ferr)
		return m, ferr
	}
	if !m.Success {
		return m, fmt.Errorf("batch failed: %d track(s) reported fatal errors", fatalTrackCount(m))
	}
	return m, nil
}

// verifyFatalBatch runs the deliberately unrepairable fixture sub-batch and
// requires a fatal, offset-anchored error. It returns nil only when the
// negative control behaves exactly as designed.
func verifyFatalBatch(input string, opts medpipe.PipelineOptions) error {
	fatalDir := filepath.Join(input, medpipe.DefaultFixtureNames().FatalDir)
	specs, err := medpipe.LoadSpecs(fatalDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no fatal control present in a hand-built input dir
		}
		return err
	}
	sub := opts
	sub.InputDir = fatalDir
	sub.WorkDir = filepath.Join(opts.WorkDir, "fatal")
	m, err := medpipe.RunBatch(specs, sub)
	if err != nil {
		return err
	}
	for _, tr := range m.Tracks {
		for _, e := range tr.Errors {
			if e.Kind == medpipe.KindFatal && e.OffsetSet {
				fmt.Printf("fatal-control ok: track=%s offset=%d code=%s\n", e.Track, e.Offset, e.Code)
				return nil
			}
		}
	}
	return fmt.Errorf("fatal control track did not produce an offset-anchored fatal error")
}

func printTracks(m *medpipe.Manifest) {
	fmt.Println("tracks:")
	for _, tr := range m.Tracks {
		rebuilt := "reused"
		if tr.IndexRebuilt {
			rebuilt = "rebuilt"
		}
		line := fmt.Sprintf("  %-16s codec=%-4s status=%-8s index=%s frames=%d keyframes=%d",
			tr.Track, tr.Codec, tr.Status, rebuilt, tr.FrameCount, tr.KeyframeCount)
		if tr.Color != nil {
			line += fmt.Sprintf(" color_conflicts=%d", tr.Color.ConflictCount())
		}
		fmt.Println(line)
	}
}

func printMetrics(m *medpipe.Manifest) {
	fmt.Println("metrics:")
	fmt.Printf("  target_bps:         %.0f\n", m.Metrics.TargetBPS)
	fmt.Printf("  actual_bps:         %.0f\n", m.Metrics.ActualBPS)
	fmt.Printf("  bitrate_delta_pct:  %.3f\n", m.Metrics.BitrateDeltaPct)
	fmt.Printf("  quality_mean:       %.2f\n", m.Metrics.QualityMean)
	fmt.Printf("  quality_min:        %.0f\n", m.Metrics.QualityMin)
	fmt.Printf("  manifest_error_ms:  %.3f\n", m.Metrics.ManifestErrorMS)
	fmt.Printf("  index_rebuilt:      %d\n", m.Metrics.IndexRebuilt)
}

func printErrors(m *medpipe.Manifest) {
	var rec, fatal int
	for _, e := range m.Errors {
		switch e.Kind {
		case medpipe.KindFatal:
			fatal++
		default:
			rec++
		}
	}
	fmt.Printf("errors: recoverable=%d fatal=%d\n", rec, fatal)
	for _, e := range m.Errors {
		loc := e.Track
		if e.OffsetSet {
			loc = fmt.Sprintf("%s@byte %d", e.Track, e.Offset)
		}
		fmt.Fprintf(os.Stderr, "  [%s] %s %s: %s — %s\n",
			strings.ToUpper(string(e.Kind)), e.Code, loc, e.Message, e.Suggestion)
	}
}

func fatalTrackCount(m *medpipe.Manifest) int {
	n := 0
	for _, tr := range m.Tracks {
		if tr.Status == medpipe.StatusFailed {
			n++
		}
	}
	return n
}
