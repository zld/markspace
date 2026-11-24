// Command mediapipeline is the terminal entry point for the media pipeline.
//
//	mediapipeline demo   --work DIR   generate fixtures, run every stage and
//	                                  write DIR/report.json
//	mediapipeline verify --work DIR   re-check the report and outputs
//	mediapipeline run    --work DIR --input FILE [--output FILE]
//	mediapipeline recover --work DIR --input FILE [--output FILE]
//
// Exit status is 0 for success, 1 for a failed verification and 2 for a usage
// or I/O error.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"mediapipeline/internal/cli"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	switch cmd {
	case "demo":
		os.Exit(runDemo(args))
	case "verify":
		os.Exit(runVerify(args))
	case "run":
		os.Exit(runPipeline(args))
	case "recover":
		os.Exit(runRecover(args))
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mediapipeline <demo|verify|run|recover> [flags]")
	fmt.Fprintln(os.Stderr, "  demo    --work DIR")
	fmt.Fprintln(os.Stderr, "  verify  --work DIR")
	fmt.Fprintln(os.Stderr, "  run     --work DIR --input FILE [--output FILE] [--resume]")
	fmt.Fprintln(os.Stderr, "  recover --work DIR --input FILE [--output FILE]")
}

func workFlag(args []string) (string, error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	work := fs.String("work", "", "work directory")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if *work == "" {
		return "", fmt.Errorf("--work is required")
	}
	return *work, nil
}

func runDemo(args []string) int {
	work, err := workFlag(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	report, err := cli.Demo(work)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(report)
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		return 1
	}
	return 0
}

func runVerify(args []string) int {
	work, err := workFlag(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	res, err := cli.Verify(work)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(res)
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify:", err)
		return 1
	}
	if !res.OK {
		return 1
	}
	return 0
}

func runPipeline(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	work := fs.String("work", "", "work directory")
	input := fs.String("input", "", "input file")
	output := fs.String("output", "", "output file")
	resume := fs.Bool("resume", false, "continue from the checkpoint")
	if err := fs.Parse(args); err != nil || *work == "" {
		fmt.Fprintln(os.Stderr, "--work is required")
		return 2
	}
	input2 := *input
	output2 := *output
	resume2 := *resume
	if input2 == "" {
		fmt.Fprintln(os.Stderr, "--input is required")
		return 2
	}
	if output2 == "" {
		output2 = filepath.Join(*work, "out", filepath.Base(input2)+".mp4")
	}
	sum, err := cli.RunOne(*work, input2, output2, resume2)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(sum)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		return 1
	}
	return 0
}

func runRecover(args []string) int {
	fs := flag.NewFlagSet("recover", flag.ContinueOnError)
	work := fs.String("work", "", "work directory")
	input := fs.String("input", "", "input file")
	output := fs.String("output", "", "output file")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if *work == "" || *input == "" {
		fmt.Fprintln(os.Stderr, "--work and --input are required")
		return 2
	}
	if *output == "" {
		*output = filepath.Join(*work, "out", filepath.Base(*input)+".mp4")
	}
	sum, err := cli.RecoverOne(*work, *input, *output)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(sum)
	if err != nil {
		fmt.Fprintln(os.Stderr, "recover:", err)
		return 1
	}
	return 0
}
