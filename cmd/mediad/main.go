// Command mediad runs the reusable media processing kernel.
//
//	mediad demo [--seed N] [--representations N] [--resume-after N] [--verify]
//	mediad verify --manifest PATH --input PATH --provenance PATH
package main

import (
	"flag"
	"fmt"
	"os"

	"mediad/internal/kernel"
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
	case "verify":
		return runVerify(args[1:])
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		usage()
		return 1
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `mediad - local media processing kernel

commands:
  demo     generate fixtures, process them and verify the result
  verify   independently verify a manifest against its input and provenance

demo flags:
  --seed N              deterministic fixture seed (default 42)
  --representations N   number of bitrate renditions (default 3)
  --resume-after N      segments committed before simulated interruption (default 2)
  --verify              run independent verification (default true)
  --work-dir PATH       keep fixtures and checkpoints in PATH
`)
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	seed := fs.Int64("seed", 42, "deterministic fixture seed")
	reps := fs.Int("representations", 3, "number of bitrate renditions")
	resumeAfter := fs.Int("resume-after", 2, "segments committed before simulated interruption")
	verify := fs.Bool("verify", true, "run independent verification")
	workDir := fs.String("work-dir", "", "directory for fixtures and checkpoints")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	cfg := kernel.DefaultConfig()
	cfg.Seed = *seed
	cfg.Representations = *reps
	cfg.ResumeAfter = *resumeAfter
	cfg.WorkDir = *workDir
	cfg.Verify = *verify
	res, err := kernel.Demo(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediad: %v\n", err)
		return int(kernel.CodeOf(err))
	}
	printResult(res)
	if res.WorkDir != "" && *workDir != "" {
		fmt.Printf("work_dir=%s\n", res.WorkDir)
	}
	if res.OK {
		return 0
	}
	if res.Failure != "" {
		fmt.Fprintf(os.Stderr, "mediad: %s\n", res.Failure)
	}
	return int(res.FailureCode)
}

func printResult(res *kernel.Result) {
	fmt.Printf("representations=%d\n", res.Reps)
	fmt.Printf("ebml_elements=%d\n", res.Elements)
	fmt.Printf("roundtrip_consistency=%.2f%%\n", res.Metrics.RoundtripConsistency*100)
	fmt.Printf("keyframe_hit_rate=%.2f%%\n", res.Metrics.KeyframeHitRate*100)
	fmt.Printf("segments=%d\n", res.Metrics.Segments)
	fmt.Printf("segment_hashes_verified=%d\n", res.SegmentHashesVerified)
	fmt.Printf("resume_recomputed=%d\n", res.Metrics.Recomputed)
	fmt.Printf("provenance_verified=%t\n", res.ProvenanceVerified)
	fmt.Printf("corrupt_cases=%d rejected=%d partial_outputs=%d\n",
		res.Corrupt.Cases, res.Corrupt.Rejected, res.Corrupt.PartialOutputs)
}
