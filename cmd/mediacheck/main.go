// Command mediacheck is the terminal entry point for the offline media
// processing kernel. It generates deterministic synthetic Matroska material,
// runs the full parse/align/manifest/index pipeline and prints a stable,
// auditable one-line summary (plus optional detail).
package main

import (
	"flag"
	"fmt"
	"os"

	"mediacheck/internal/ebml"
	"mediacheck/internal/fixture"
	"mediacheck/internal/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 1
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage()
		return 1
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `mediacheck - offline Matroska/WebM manifest kernel

usage:
  mediacheck demo [flags]

flags:
  --seed int              deterministic generator seed (default 7)
  --rates int             number of representations/bitrates (default 3)
  --segments int          clusters/segments per representation (default 12)
  --checkpoint-every int  checkpoint cadence in chunks (default 4)
  --corrupt string        fault class: cluster-truncate | missing-track |
                          wrap-drift | vfr-misalign | unknown-nesting
  --interrupt-after int   simulate crash before this chunk (0 disables)
  --resume                resume from the last complete checkpoint
  --dry-run               process without writing checkpoint files
  --workdir string        checkpoint directory (default temp dir)
  --detail                print per-segment detail after the summary

exit codes: 0 ok | 2 material/timeline fault | 3 index/recovery fault
`)
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	var (
		seed            = fs.Int64("seed", 7, "generator seed")
		rates           = fs.Int("rates", 3, "representations")
		segments        = fs.Int("segments", 12, "segments per representation")
		checkpointEvery = fs.Int("checkpoint-every", 4, "checkpoint cadence")
		corrupt         = fs.String("corrupt", "", "fault class")
		interruptAfter  = fs.Int("interrupt-after", 0, "simulated crash chunk")
		resume          = fs.Bool("resume", false, "resume from checkpoint")
		dryRun          = fs.Bool("dry-run", false, "no checkpoint writes")
		workdir         = fs.String("workdir", "", "checkpoint directory")
		detail          = fs.Bool("detail", false, "per-segment detail")
	)
	if err := fs.Parse(args); err != nil {
		return 1
	}

	dir := *workdir
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "mediacheck-")
		if err != nil {
			fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
			return 3
		}
		if *workdir == "" {
			defer os.RemoveAll(dir)
		}
	}

	cfg := pipeline.Config{
		Seed: *seed, Rates: *rates, Clusters: *segments,
		CheckpointEvery: *checkpointEvery, InterruptAfter: *interruptAfter,
		Resume: *resume, DryRun: *dryRun, WorkDir: dir,
		Corrupt:         fixture.Corruption(*corrupt),
		TargetSegmentNS: 1_000_000_000,
		MaxSkewNS:       20_000_000,
		DriftLimitPPM:   500,
	}

	res, err := pipeline.Run(cfg)
	if err != nil {
		return reportError(err, res)
	}

	fmt.Printf("representations=%d segments_each=%d boundary_skew_ms<=%d manifest_sha256=%s\n",
		res.Representations, res.SegmentsEach, res.BoundarySkewMS, res.ManifestSHA)
	if *resume || *interruptAfter > 0 {
		// Required auditable form: the bound token plus the observed replay count.
		fmt.Printf("replayed_chunks<=%d replayed_actual=%d recovery_ms=%d output_sha256=%s\n",
			cfg.CheckpointEvery, res.ReplayedChunks, res.RecoveryMS, res.ManifestSHA)
	}
	if *detail && res.Manifest != nil {
		for _, s := range res.Manifest.Segments {
			fmt.Printf("  seg=%02d t0_ns=%d duration_ns=%d\n", s.Index, s.T0NS, s.DurationNS)
		}
	}
	return 0
}

// reportError renders the mandated machine-readable fault line and returns the
// process exit code carried by the typed pipeline error.
func reportError(err error, res pipeline.Result) int {
	pe, ok := err.(*pipeline.Error)
	if !ok {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		return 3
	}
	switch pe.Exit {
	case pipeline.ExitMaterial:
		class := pe.Class
		if class == "" {
			class = ebml.ErrBadElement
		}
		fmt.Printf("error=%s offset=%d recovery_checkpoint=%d\n",
			class, pe.Offset, res.RecoveryCheckpoint)
		return 2
	case pipeline.ExitIndex:
		fmt.Printf("error=INDEX recovery_checkpoint=%d detail=%s\n",
			res.RecoveryCheckpoint, pe.Msg)
		return 3
	default:
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		return 3
	}
}
