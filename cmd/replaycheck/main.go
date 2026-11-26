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
	"path/filepath"
	"syscall"
	"time"

	"replayproof/internal/media"
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

	sum, err := replay.RunVerify(ctx, cfg)
	if sum == nil {
		fmt.Fprintf(os.Stderr, "replaycheck: %v\n", err)
		return exitFor(err)
	}
	sumPath := filepath.Join(*out, "summary.txt")
	digest, werr := replay.WriteSummary(sum, sumPath)
	if werr != nil && err == nil {
		err = werr
	}
	printSummary(sum, digest, sumPath)
	return exitFor(err)
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
	cfg.PreRoll = media.FromMicros(pre.Microseconds())
	cfg.PostRoll = media.FromMicros(post.Microseconds())
	cfg.Selectors = replay.Selectors(selectors)
	inputs, err := replay.GenerateInputs(cfg.Seed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "replaycheck: %v\n", err)
		return exitFailure
	}
	inputs.PrimaryMP4 = mp4Data
	inputs.PrimaryMXF = mxfData

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sum, err := replay.Run(ctx, cfg, inputs)
	if sum == nil {
		fmt.Fprintf(os.Stderr, "replaycheck: %v\n", err)
		return exitFor(err)
	}
	digest, werr := replay.WriteSummary(sum, filepath.Join(*out, "summary.txt"))
	if werr != nil && err == nil {
		err = werr
	}
	printSummary(sum, digest, filepath.Join(*out, "summary.txt"))
	return exitFor(err)
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

func printSummary(sum *output.Summary, digest, path string) {
	for _, line := range summaryLines(sum) {
		fmt.Println(line)
	}
	if digest != "" {
		fmt.Printf("summary_path=%s\n", path)
		fmt.Printf("digest_sha256=%s\n", digest)
	}
}

func summaryLines(sum *output.Summary) []string {
	var out []string
	for _, m := range sum.Maps {
		out = append(out, m.Line())
	}
	for _, md := range sum.Metadata {
		out = append(out, fmt.Sprintf("meta field=%s source=%s value=%s", md.Field, md.Source, md.Value))
	}
	for _, c := range sum.Clips {
		out = append(out, fmt.Sprintf("clip event=%d path=%s bytes=%d sha256=%s",
			c.Spec.EventIndex, c.Path, c.Bytes, c.Hash))
	}
	for _, bc := range sum.BadCases {
		out = append(out, bc.Line())
	}
	out = append(out, sum.KeyMetrics())
	return out
}

func exitFor(err error) int {
	if err == nil {
		return exitOK
	}
	code := media.CodeOf(err)
	if code == media.CodeCanceled || errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "replaycheck: canceled: %v\n", err)
		return exitCancel
	}
	if me, ok := media.AsError(err); ok {
		switch me.Code {
		case media.CodeMETAConflict, media.CodeTruncatedBox, media.CodeInvalidBox,
			media.CodeMXFOutOfRange, media.CodeMXFMalformed, media.CodeNoTrack,
			media.CodeBadTimestamp, media.CodeEventWindow, media.CodeNoKeyframe,
			media.CodeCheckpointMismatch:
			fmt.Fprintf(os.Stderr, "replaycheck: %s: %v\n", me.Code, err)
			return exitInput
		}
	}
	fmt.Fprintf(os.Stderr, "replaycheck: %v\n", err)
	return exitFailure
}
