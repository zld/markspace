package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"media-archiver/internal/media"
	"media-archiver/internal/pipeline"
	"media-archiver/internal/samplegen"
)

func main() {
	code := run(os.Args[1:])
	fmt.Printf("kernel_exit_code=%d\n", code)
	os.Exit(code)
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "resume":
		return runResume(args[1:])
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		usage()
		return 2
	}
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	seed := fs.Int64("seed", 17, "deterministic fixture seed")
	workdir := fs.String("workdir", "./tmp/demo", "local working directory")
	inject := fs.String("inject", "none", "none|truncate|backward|duplicate|bad-index")
	interruptAfter := fs.String("interrupt-after", "", "parse|transform|index")
	lookupFrame := fs.Int64("lookup-frame", -1, "optional global frame to locate")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	stage, err := parseInterruptStage(*interruptAfter)
	if err != nil {
		printBootstrapError(err)
		return media.ExitCode(err)
	}
	expected, err := pipeline.PrepareDemoInput(*workdir, *seed, *inject)
	if err != nil {
		printBootstrapError(err)
		return media.ExitCode(err)
	}

	lookup := expected.TargetFrame
	if *lookupFrame >= 0 {
		lookup = *lookupFrame
	}
	cfg := pipeline.Config{
		WorkDir:        *workdir,
		Processing:     pipeline.DefaultProcessing(*seed, *inject),
		InterruptAfter: stage,
		LookupFrame:    &lookup,
	}

	if *inject == "bad-index" {
		return runBadIndexDemo(cfg, expected)
	}

	rep, err := pipeline.Run(cfg)
	if rep != nil {
		rep.Expected = expected
		rep.PrintKeyValues()
	} else if err != nil {
		printBootstrapError(err)
	}
	return media.ExitCode(err)
}

func runBadIndexDemo(cfg pipeline.Config, expected *samplegen.Expected) int {
	first, err := pipeline.Run(cfg)
	if err != nil {
		if first != nil {
			first.PrintKeyValues()
		}
		return media.ExitCode(err)
	}
	audioBefore := first.AudioSHA256
	if err := pipeline.CorruptIndexForDemo(cfg.WorkDir); err != nil {
		printBootstrapError(media.NewError(media.CodeInternal, media.StageIndex, -1, "corrupt demo index", err))
		return 1
	}

	second, err := pipeline.Run(cfg)
	if second != nil {
		second.Expected = expected
		second.PrintKeyValues()
	}
	if err != nil {
		return media.ExitCode(err)
	}
	if !second.IndexRebuilt {
		fmt.Println("index_rebuilt=false")
		return 1
	}
	if second.AudioSHA256 != audioBefore {
		fmt.Printf("audio_sha_changed before=%s after=%s\n", audioBefore, second.AudioSHA256)
		return 1
	}
	fmt.Println("audio_digest_unchanged=true")
	return 0
}

func runResume(args []string) int {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	workdir := fs.String("workdir", "./tmp/recover", "interrupted working directory")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	cfg := pipeline.Config{WorkDir: *workdir, Resume: true}
	if expected, err := pipeline.LoadExpected(*workdir); err == nil {
		cfg.LookupFrame = &expected.TargetFrame
		rep, runErr := pipeline.Run(cfg)
		if rep != nil {
			rep.Expected = expected
			rep.PrintKeyValues()
		} else if runErr != nil {
			printBootstrapError(runErr)
		}
		return media.ExitCode(runErr)
	}

	rep, err := pipeline.Run(cfg)
	if rep != nil {
		rep.PrintKeyValues()
	} else if err != nil {
		printBootstrapError(err)
	}
	return media.ExitCode(err)
}

func parseInterruptStage(value string) (media.Stage, error) {
	switch value {
	case "":
		return "", nil
	case "parse":
		return media.StageParse, nil
	case "transform":
		return media.StageTransform, nil
	case "index":
		return media.StageIndex, nil
	default:
		return "", media.NewError(media.CodeInvalidConfig, "", -1, "interrupt-after must be parse, transform, or index", nil)
	}
}

func printBootstrapError(err error) {
	me := media.AsError(err)
	if me == nil {
		fmt.Printf("error_code=INTERNAL_ERROR\nstage=\ncorrupt_offset=-1\n%s\n", err)
		return
	}
	fmt.Printf("error_code=%s\nstage=%s\ncorrupt_offset=%d\nreason=%s\n", me.Code, me.Stage, me.Offset, me.Reason)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: archiver demo --seed N --workdir DIR [--inject none|truncate|backward|duplicate|bad-index] [--interrupt-after parse|transform|index]")
	fmt.Fprintln(os.Stderr, "       archiver resume --workdir DIR")
}
