package main

import (
	"flag"
	"fmt"
	"io"

	"mediarc/internal/model"
	"mediarc/internal/pipeline"
)

func runResume(argv []string, stdout io.Writer) (int, error) {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workdir := fs.String("workdir", "", "checkpoint working directory")
	interruptAfter := fs.Int("interrupt-after", 0, "exit 10 after this many newly committed chunks")
	avOffsetMS := fs.Int64("av-offset-ms", 0, "override A/V start offset in milliseconds")
	sourceLayout := fs.String("source-layout", "", "override source channel layout")
	targetLayout := fs.String("target-layout", "", "override target channel layout")
	if err := fs.Parse(argv); err != nil {
		return 1, err
	}
	if *workdir == "" {
		return 1, fmt.Errorf("resume requires --workdir")
	}
	o, err := pipeline.LoadOptions(*workdir, *interruptAfter)
	if err != nil {
		return 1, fmt.Errorf("load config; run demo first: %w", err)
	}
	// Explicit CLI overrides are useful for negative acceptance scenarios.
	setFlags := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })
	if setFlags["av-offset-ms"] {
		o.AVStartOffsetMS = *avOffsetMS
	}
	if *sourceLayout != "" {
		o.SourceLayout = *sourceLayout
	}
	if *targetLayout != "" {
		o.TargetLayout = *targetLayout
	}
	res, runErr := pipeline.Run(o)
	if res != nil {
		res.Summary.Print(stdout)
	}
	code := model.ExitCode(runErr)
	if runErr != nil {
		return code, runErr
	}
	return code, nil
}
