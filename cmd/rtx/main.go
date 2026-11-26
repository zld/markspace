// Command rtx repairs weak-network RTP/CMAF captures offline.
//
// Subcommands:
//
//	rtx fixtures -work DIR            write the example datasets
//	rtx run -input DIR -cache DIR -checkpoint FILE -out DIR [-stop-after N]
package main

import (
	"flag"
	"fmt"
	"os"

	"rtx/internal/fixtures"
	"rtx/internal/model"
	"rtx/internal/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const usage = `rtx - weak-network RTP/CMAF offline repair

usage:
  rtx fixtures -work DIR
  rtx run -input DIR -cache DIR -checkpoint FILE -out DIR [-stop-after N] [-inject-interrupt N]

run prints one line per processed record, then the evidence and report summaries.
exit codes: 0 ok, 1 failure, 2 truncated input, 64 usage, 75 interrupted.
`

func run(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 64
	}
	switch args[0] {
	case "fixtures":
		return cmdFixtures(args[1:], stdout, stderr)
	case "run":
		return cmdRun(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "rtx: unknown command %q\n%s", args[0], usage)
		return 64
	}
}

func cmdFixtures(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("fixtures", flag.ContinueOnError)
	fs.SetOutput(stderr)
	work := fs.String("work", "", "directory to write fixture datasets into")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if *work == "" {
		fmt.Fprintln(stderr, "rtx fixtures: -work is required")
		return 64
	}
	scenarios, err := fixtures.Generate(*work, fixtures.DefaultOptions())
	if err != nil {
		fmt.Fprintf(stderr, "rtx fixtures: %v\n", err)
		return 1
	}
	for _, sc := range scenarios {
		fmt.Fprintf(stdout, "fixture=%s dir=%s\n", sc.Name, sc.Dir)
	}
	fmt.Fprintf(stdout, "fixtures_ok=1 count=%d\n", len(scenarios))
	return 0
}

func cmdRun(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	input := fs.String("input", "", "input directory containing manifest.json")
	cacheDir := fs.String("cache", "", "cache directory")
	checkpoint := fs.String("checkpoint", "", "checkpoint file")
	out := fs.String("out", "", "output directory")
	stopAfter := fs.Int("stop-after", 0, "stop after this many records")
	inject := fs.Int("inject-interrupt", -1, "simulate a crash after N records (testing)")
	noRepair := fs.Bool("no-repair", false, "disable tfdt repair")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if *input == "" || *cacheDir == "" || *checkpoint == "" || *out == "" {
		fmt.Fprintln(stderr, "rtx run: -input, -cache, -checkpoint and -out are required")
		return 64
	}
	opts := pipeline.Options{
		InputDir:   *input,
		CacheDir:   *cacheDir,
		Checkpoint: *checkpoint,
		OutDir:     *out,
		StopAfter:  *stopAfter,
		NoRepair:   *noRepair,
		Progress: func(line string) {
			fmt.Fprintf(stdout, "%s\n", line)
		},
	}
	if *inject >= 0 {
		opts.OnRecord = func(done, total int) error {
			if done == *inject {
				// Simulate a hard crash after the checkpoint for this record
				// has already advanced, by returning a sentinel error.
				return fmt.Errorf("%w: injected after %d/%d", errInjected, done, total)
			}
			return nil
		}
	}
	res, err := pipeline.Run(opts)
	if err != nil {
		printFailure(stdout, stderr, err)
		return codeOf(err)
	}
	printSuccess(stdout, res)
	return 0
}

var errInjected = fmt.Errorf("injected interrupt")

func printFailure(stdout, stderr *os.File, err error) {
	if e, ok := model.As(err); ok {
		fmt.Fprintf(stdout, "error code=%s %s\n", e.Code, e.Error())
		if e.HasByte {
			fmt.Fprintf(stdout, "first_unreadable_offset=%d\n", e.Offset)
		}
		fmt.Fprintf(stderr, "rtx: %s\n", e.Error())
		return
	}
	fmt.Fprintf(stdout, "error code=internal %v\n", err)
	fmt.Fprintf(stderr, "rtx: %v\n", err)
}

func codeOf(err error) int {
	return model.ExitCodeOf(err)
}

func printSuccess(stdout *os.File, res *pipeline.Result) {
	fmt.Fprintf(stdout, "records=%d cursor=%d repeat_equal=%d resume=%d\n",
		res.Records, res.Cursor, boolToInt(res.RepeatEqual), boolToInt(res.Resume))
	fmt.Fprintf(stdout, "gap=%d tfdt_rollback=%d source_switch=%d\n",
		res.Gaps, res.Repairs, res.SourceSwitch)
	if res.RTP != nil {
		for _, s := range res.RTP.Summaries {
			fmt.Fprintf(stdout, "%s\n", s.RTPLine())
		}
	}
	if res.Metrics != nil {
		fmt.Fprintf(stdout, "%s\n", res.Metrics.SummaryLine())
		if err := res.Metrics.Validate(96, 2, -6, 1, 2); err != nil {
			fmt.Fprintf(stdout, "evidence_check=0 %v\n", err)
		} else {
			fmt.Fprintf(stdout, "evidence_check=1\n")
		}
	}
	fmt.Fprintf(stdout, "index_sha256=%s media_sha256=%s\n", res.IndexSum, res.MediaSum)
	for i, d := range res.FragmentDigests {
		fmt.Fprintf(stdout, "fragment_sha256[%d]=%s\n", i+1, d)
	}
	fmt.Fprintf(stdout, "cache_hits=%d cache_misses=%d\n", res.CacheHits, res.CacheMisses)
	fmt.Fprintf(stdout, "run_ok=1\n")
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
