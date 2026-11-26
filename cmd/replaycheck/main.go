// Command replaycheck is a thin terminal front end over the replay package.
// All processing lives in the library; this file only parses flags, prints
// and selects an exit code.
//
//	exit 0   verification or processing succeeded
//	exit 2   corrupt input or unresolved metadata conflict
//	exit 130 cancellation
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"replayproof/internal/output"
	"replayproof/replay"
)

const (
	exitOK      = 0
	exitInput   = 2
	exitCancel  = 130
	exitFailure = 1
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitFailure
	}
	switch args[0] {
	case "verify":
		return runVerify(args[1:])
	case "process":
		return runProcess(args[1:])
	case "-h", "--help", "help":
		usage()
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "replaycheck: unknown command %q\n", args[0])
		usage()
		return exitFailure
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: replaycheck <command> [flags]

commands:
  verify    generate fixtures, process them and prove the acceptance metrics
  process   run the replay core over caller-supplied files

verify flags:
  --seed int         fixture jitter seed (default 7)
  --out string       output directory (required)
  --workers int      pipeline workers (default 3)
  --queue int        pipeline queue capacity (default 2)
  --delay duration   producer delay between jobs (default 20ms)

process flags:
  --mp4 string       primary MP4 path
  --mxf string       primary MXF path
  --out string       output directory (required)
  --workers int      pipeline workers (default 3)
  --queue int        pipeline queue capacity (default 2)
  --pre duration     pre-roll window (default 400ms)
  --post duration    post-roll window (default 300ms)
  --select field=src metadata source selector, repeatable (rotation, crop, color)
`)
}

func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	seed := fs.Int64("seed", 7, "fixture jitter seed")
	out := fs.String("out", "", "output directory")
	workers := fs.Int("workers", 3, "pipeline workers")
	queue := fs.Int("queue", 2, "pipeline queue capacity")
	delay := fs.Duration("delay", 20*time.Millisecond, "producer delay between jobs")
	if err := fs.Parse(args); err != nil {
		return exitFailure
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "replaycheck: --out is required")
		return exitFailure
	}

	cfg := replay.DefaultConfig()
	cfg.Seed = *seed
	cfg.Workers = *workers
	cfg.Queue = *queue
	cfg.DelayNS = delay.Nanoseconds()
	cfg.OutDir = *out

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return execute(ctx, func(ctx context.Context) (*output.Summary, error) {
		return replay.RunVerify(ctx, cfg)
	}, *out)
}

// execute runs one library operation, writes the summary and maps the result
// to an exit code. Both commands share it so their output is identical.
func execute(ctx context.Context, fn func(context.Context) (*output.Summary, error), outDir string) int {
	sum, err := fn(ctx)
	if sum == nil {
		fmt.Fprintf(os.Stderr, "replaycheck: %v\n", err)
		return replay.ExitCode(err)
	}
	sumPath := replay.SummaryPath(outDir)
	digest, werr := replay.WriteSummary(sum, sumPath)
	if werr != nil && err == nil {
		err = werr
	}
	if digest != "" {
		// Print the published summary verbatim so terminal output and the
		// file an operator diffs are identical, line for line.
		if text, rerr := os.ReadFile(sumPath); rerr == nil {
			os.Stdout.Write(text)
		}
		fmt.Printf("summary_path=%s\n", sumPath)
		fmt.Printf("digest_sha256=%s\n", digest)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "replaycheck: %v\n", err)
	}
	return replay.ExitCode(err)
}

func runProcess(args []string) int {
	fs := flag.NewFlagSet("process", flag.ContinueOnError)
	mp4 := fs.String("mp4", "", "primary MP4 path")
	mxf := fs.String("mxf", "", "primary MXF path")
	out := fs.String("out", "", "output directory")
	workers := fs.Int("workers", 3, "pipeline workers")
	queue := fs.Int("queue", 2, "pipeline queue capacity")
	pre := fs.Duration("pre", 400*time.Millisecond, "pre-roll")
	post := fs.Duration("post", 300*time.Millisecond, "post-roll")
	var selectors flagSet
	fs.Var(&selectors, "select", "metadata source selector field=source (repeatable)")
	if err := fs.Parse(args); err != nil {
		return exitFailure
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "replaycheck: --out is required")
		return exitFailure
	}
	if *mp4 == "" || *mxf == "" {
		fmt.Fprintln(os.Stderr, "replaycheck: --mp4 and --mxf are required")
		return exitFailure
	}
	mp4Data, err := os.ReadFile(*mp4)
	if err != nil {
		fmt.Fprintf(os.Stderr, "replaycheck: %v\n", err)
		return exitInput
	}
	mxfData, err := os.ReadFile(*mxf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "replaycheck: %v\n", err)
		return exitInput
	}
	cfg := replay.DefaultConfig()
	cfg.OutDir = *out
	cfg.Workers = *workers
	cfg.Queue = *queue
	cfg.PreRoll = replay.FromDuration(*pre)
	cfg.PostRoll = replay.FromDuration(*post)
	cfg.Selectors = replay.Selectors(selectors)
	inputs, err := replay.GenerateInputs(cfg.Seed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "replaycheck: %v\n", err)
		return exitFailure
	}
	// The caller supplies the primary pair; the generated corrupt and
	// conflicting cases stay in place so the failure paths are still
	// exercised and reported.
	inputs.PrimaryMP4 = mp4Data
	inputs.PrimaryMXF = mxfData

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return execute(ctx, func(ctx context.Context) (*output.Summary, error) {
		return replay.Run(ctx, cfg, inputs)
	}, *out)
}

// flagSet collects repeated key=value selectors.
type flagSet map[string]string

func (f *flagSet) String() string {
	if f == nil {
		return ""
	}
	s := ""
	for k, v := range *f {
		if s != "" {
			s += ","
		}
		s += k + "=" + v
	}
	return s
}

func (f *flagSet) Set(v string) error {
	for i := 0; i < len(v); i++ {
		if v[i] == '=' && i > 0 && i < len(v)-1 {
			if *f == nil {
				*f = make(map[string]string)
			}
			(*f)[v[:i]] = v[i+1:]
			return nil
		}
	}
	return errors.New("selector must be field=source")
}
