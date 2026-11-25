// Command mediapipeline is the terminal acceptance adapter for the fragment
// manifest pipeline. It generates deterministic samples, runs the bounded
// streaming pipeline, and prints stable key=value results. Exit codes follow
// the spec: 0 success, 1 runtime/media failure, 2 invalid arguments.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"mediapipeline/internal/app"
	"mediapipeline/internal/checkpoint"
	"mediapipeline/internal/fault"
	"mediapipeline/internal/fixture"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return exitInvalid
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "-h", "--help", "help":
		printUsage(os.Stdout)
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		printUsage(os.Stderr)
		return exitInvalid
	}
}

const (
	exitOK      = 0
	exitRuntime = 1
	exitInvalid = 2
)

// flags is a minimal deterministic flag parser (no flag package surprises).
type flags struct {
	values map[string]string
	bools  map[string]bool
}

func parseFlags(args []string, allowed map[string]bool) (*flags, error) {
	f := &flags{values: map[string]string{}, bools: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			return nil, fmt.Errorf("unexpected argument %q", a)
		}
		key := strings.TrimPrefix(a, "--")
		if _, ok := allowed[key]; !ok {
			return nil, fmt.Errorf("unknown flag --%s", key)
		}
		if isBoolFlag(key) {
			f.bools[key] = true
			continue
		}
		if i+1 >= len(args) {
			return nil, fmt.Errorf("flag --%s requires a value", key)
		}
		f.values[key] = args[i+1]
		i++
	}
	return f, nil
}

func isBoolFlag(key string) bool {
	return key == "resume"
}

func (f *flags) intOr(key string, def int) (int, error) {
	v, ok := f.values[key]
	if !ok {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("--%s must be an integer, got %q", key, v)
	}
	return n, nil
}

func runDemo(args []string) int {
	allowed := map[string]bool{
		"input": true, "case": true, "representations": true, "queue": true,
		"interrupt-after": true, "resume": false, "repeat": true,
	}
	fl, err := parseFlags(args, allowed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error=invalid_argument detail=%q\n", err.Error())
		return exitInvalid
	}
	reps, err := fl.intOr("representations", 2)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error=invalid_argument detail=%q\n", err.Error())
		return exitInvalid
	}
	queue, err := fl.intOr("queue", 2)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error=invalid_argument detail=%q\n", err.Error())
		return exitInvalid
	}
	repeat, err := fl.intOr("repeat", 1)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error=invalid_argument detail=%q\n", err.Error())
		return exitInvalid
	}
	if reps < 2 || queue < 1 || repeat < 1 {
		fmt.Fprintln(os.Stderr, "error=invalid_argument detail=\"representations>=2, queue>=1, repeat>=1\"")
		return exitInvalid
	}
	caseName := fixture.Case(fl.values["case"])
	if caseName == "" {
		caseName = fixture.CaseNormal
	}
	if !validCase(caseName) {
		fmt.Fprintf(os.Stderr, "error=invalid_argument detail=\"unknown case %s\"\n", caseName)
		return exitInvalid
	}
	interrupt := checkpoint.Stage(fl.values["interrupt-after"])
	if interrupt != "" && !validStage(interrupt) {
		fmt.Fprintf(os.Stderr, "error=invalid_argument detail=\"unknown stage %s\"\n", interrupt)
		return exitInvalid
	}
	inputDir := fl.values["input"]
	if inputDir == "" {
		inputDir = filepathJoin("generated")
	}
	outputDir := filepathJoin("tmp")

	var last *app.Summary
	var runErr error
	for i := 0; i < repeat; i++ {
		cfg := app.Config{
			InputDir:        inputDir,
			OutputDir:       outputDir,
			Case:            caseName,
			Representations: reps,
			Fragments:       5,
			QueueCapacity:   queue,
			InterruptAfter:  interrupt,
			Resume:          fl.bools["resume"],
		}
		p, err := app.New(cfg)
		if err != nil {
			printFault(err)
			return exitInvalid
		}
		last, runErr = p.Run(context.Background())
		if runErr != nil {
			printSummary(last)
			printFault(runErr)
			if fault.Is(runErr, fault.CodeInvalidArgument) {
				return exitInvalid
			}
			return exitRuntime
		}
	}
	printSummary(last)
	return exitOK
}

func runVerify(args []string) int {
	allowed := map[string]bool{"manifest": true}
	fl, err := parseFlags(args, allowed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error=invalid_argument detail=%q\n", err.Error())
		return exitInvalid
	}
	path := fl.values["manifest"]
	if path == "" {
		fmt.Fprintln(os.Stderr, "error=invalid_argument detail=\"--manifest is required\"")
		return exitInvalid
	}
	res, err := app.Verify(path)
	if err != nil {
		if res != nil {
			printVerify(res)
		}
		printFault(err)
		if fault.Is(err, fault.CodeInvalidArgument) {
			return exitInvalid
		}
		return exitRuntime
	}
	printVerify(res)
	return exitOK
}

func filepathJoin(parts ...string) string {
	return strings.Join(parts, string(os.PathSeparator))
}
