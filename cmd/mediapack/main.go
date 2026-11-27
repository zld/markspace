// Command mediapack is the terminal-only acceptance entry point. It drives the
// reusable pipeline packages to generate samples, process MXF/MP4 inputs and
// print JSON evidence; it holds no media-processing logic itself.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"mediapack/internal/fixture"
	"mediapack/internal/pipeline"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	case "generate":
		os.Exit(runGenerate(os.Args[2:]))
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr,
		"usage: mediapack demo --out DIR [--scenario normal|corrupt|recovery]\n"+
			"       mediapack generate --out DIR")
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	outDir := fs.String("out", "", "output directory (required)")
	scenario := fs.String("scenario", "all",
		"all|normal|corrupt|recovery")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *outDir == "" {
		fmt.Fprintln(os.Stderr, "demo: --out is required")
		return 2
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	set, err := fixture.BuildDemoSet(
		filepath.Join(*outDir, "sample.mxf"),
		filepath.Join(*outDir, "sample.mp4"),
		filepath.Join(*outDir, "corrupt.mp4"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}

	switch *scenario {
	case "normal":
		return demoNormal(set, *outDir)
	case "corrupt":
		return demoCorrupt(set)
	case "recovery":
		return demoRecovery()
	case "all":
		// Every scenario runs; each section reports the exit code that the
		// standalone scenario produces. The wrapper exits 0 when all behave
		// as designed, including correct detection of the corrupt input.
		codeNormal := demoNormal(set, *outDir)
		codeCorrupt := demoCorrupt(set)
		codeRecovery := demoRecovery()
		fmt.Printf("\nscenario exit codes: normal=%d corrupt=%d recovery=%d\n",
			codeNormal, codeCorrupt, codeRecovery)
		if codeNormal != 0 || codeCorrupt != 2 || codeRecovery != 0 {
			return 3
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown scenario %q\n", *scenario)
		return 2
	}
}

func demoNormal(set *fixture.DemoSet, outDir string) int {
	outPath := filepath.Join(outDir, "out.mpkm")
	res, err := pipeline.NormalRun(set, outPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	fmt.Println("=== normal scenario ===")
	printSummary(res.Summary)
	writeJSON(filepath.Join(outDir, "evidence-normal.json"), res.Summary)
	return res.ExitCode
}

func demoCorrupt(set *fixture.DemoSet) int {
	fmt.Println("=== corrupt scenario ===")
	res := pipeline.CorruptRun(set, "")
	printSummary(res.Summary)
	if res.Summary.Failure != nil {
		fmt.Printf("first illegal offset: %d (track %d, code %d)\n",
			res.Summary.Failure.Offset, res.Summary.Failure.Track,
			res.Summary.Failure.Code)
	}
	return res.ExitCode
}

func demoRecovery() int {
	fmt.Println("=== recovery scenario ===")
	sc := fixture.BuildRecoveryScenario()
	res, err := pipeline.RecoveryRun(sc)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	printSummary(res.Summary)
	fmt.Printf("replay count: %d (wrap=%d overlap=%d gap=%d)\n",
		res.Summary.ReplayCount, res.Summary.WrapCount,
		res.Summary.OverlapCount, res.Summary.GapCount)
	if res.Summary.ReplayCount == 0 {
		fmt.Fprintln(os.Stderr, "expected replay count > 0")
		return 3
	}
	return res.ExitCode
}

func runGenerate(args []string) int {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	outDir := fs.String("out", "", "output directory (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *outDir == "" {
		fmt.Fprintln(os.Stderr, "generate: --out is required")
		return 2
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	set, err := fixture.BuildDemoSet(
		filepath.Join(*outDir, "sample.mxf"),
		filepath.Join(*outDir, "sample.mp4"),
		filepath.Join(*outDir, "corrupt.mp4"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	if err := os.WriteFile(filepath.Join(*outDir, "sample.mxf"), set.MXFRaw, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	if err := os.WriteFile(filepath.Join(*outDir, "sample.mp4"), set.MP4Raw, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	if err := os.WriteFile(filepath.Join(*outDir, "corrupt.mp4"), set.CorruptRaw, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	fmt.Printf("generated samples in %s\n", *outDir)
	return 0
}

func printSummary(s interface{}) {
	pretty, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	fmt.Println(string(pretty))
}

func writeJSON(path string, v interface{}) {
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	if err := os.WriteFile(path, pretty, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}
