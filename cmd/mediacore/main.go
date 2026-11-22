// Command mediacore is the terminal adapter for the media processing kernel.
//
// Usage:
//
//	mediacore demo [--case NAME] [--frames N] --out DIR
//	mediacore verify --input FRAMES --index INDEX --manifest MANIFEST
//
// Exit codes: 0 verified, 1 damaged material or statistics mismatch,
// 2 bad parameters or file operation failure.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediacore/internal/demo"
	"mediacore/internal/verify"
	"mediacore/manifest"
	"mediacore/media"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
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
		fmt.Fprintf(os.Stderr, "mediacore: unknown command %q\n\n", args[0])
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `mediacore - deterministic media processing kernel

Commands:
  demo    Generate sample material, process it, write an index and manifest.
  verify  Independently re-check a frames stream, its index and manifest.

Demo flags:
  --case NAME   clean | burst-loss | corrupt | truncated | wrap | digest-shift | packets
  --frames N    number of frames for the clean sample (default 1200)
  --out DIR     output directory (required)
  --json        print the report as JSON

Verify flags:
  --input PATH      frame stream file (required)
  --index PATH      index file (required)
  --manifest PATH   manifest file (required)
  --json            print the report as JSON

Exit codes: 0 verified, 1 damaged material or statistics mismatch, 2 bad parameters/files.
`)
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	caseName := fs.String("case", demo.CaseClean, "demo case")
	outDir := fs.String("out", "", "output directory (required)")
	frames := fs.Int("frames", 0, "frame count override")
	asJSON := fs.Bool("json", false, "print report as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*outDir) == "" {
		fmt.Fprintln(os.Stderr, "mediacore demo: --out is required")
		return 2
	}
	rep, code, err := demo.Run(demo.Options{
		Case:   *caseName,
		OutDir: *outDir,
		Frames: *frames,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediacore demo: %s\n", media.DescribeError(err))
		if rep != nil && *asJSON {
			emitJSON(rep)
		}
		return code
	}
	if *asJSON && rep != nil {
		emitJSON(rep)
		return code
	}
	printReport(rep)
	return code
}

func printReport(rep *demo.Report) {
	if rep == nil {
		return
	}
	fmt.Printf("case=%s out=%s\n", rep.Case, rep.OutputDir)
	if rep.Frames > 0 || rep.Segments > 0 {
		fmt.Printf("frames=%d segments=%d silence_ms=%d duplicate_groups=%d\n",
			rep.Frames, rep.Segments, rep.SilenceMS, rep.DuplicateGroups)
		fmt.Printf("target_kbps=%d actual_kbps=%d deviation_pct=%.2f\n",
			rep.TargetKBPS, rep.ActualKBPS, rep.DeviationPct)
		if rep.AlignmentMS > 0 || rep.ManifestDigest != "" {
			fmt.Printf("alignment_error_ms=%d\n", rep.AlignmentMS)
		}
	}
	if rep.MissingFrames > 0 || rep.GapCount > 0 {
		fmt.Printf("gaps=%d missing_frames=%d\n", rep.GapCount, rep.MissingFrames)
	}
	if rep.Duplicates > 0 || rep.Late > 0 {
		fmt.Printf("duplicate_packets=%d late_packets=%d\n", rep.Duplicates, rep.Late)
	}
	if rep.DamageOffset > 0 {
		fmt.Printf("damage_offset=%d\n", rep.DamageOffset)
	}
	if rep.IndexCache != nil {
		fmt.Printf("index_cache=%s rebuilt=%t %s\n", rep.IndexCache.State, rep.IndexCache.Rebuilt, rep.IndexCache.Reason)
	}
	for _, n := range rep.Notes {
		fmt.Println(n)
	}
	if rep.ManifestDigest != "" {
		fmt.Printf("manifest_digest=%s\n", short(rep.ManifestDigest))
	}
	if rep.Recovered {
		fmt.Println("recovered=true")
	}
	for _, f := range rep.Failures {
		fmt.Printf("failure field=%s category=%s expected=%s actual=%s %s\n",
			f.Field, f.Category, f.Expected, f.Actual, f.Detail)
	}
	if rep.ExitCode == 0 && len(rep.Failures) == 0 {
		fmt.Println("result=ok")
	}
}

func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	input := fs.String("input", "", "frame stream file (required)")
	indexPath := fs.String("index", "", "index file (required)")
	manifestPath := fs.String("manifest", "", "manifest file (required)")
	asJSON := fs.Bool("json", false, "print report as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *input == "" || *indexPath == "" || *manifestPath == "" {
		fmt.Fprintln(os.Stderr, "mediacore verify: --input, --index and --manifest are required")
		return 2
	}
	if err := checkPaths([]string{*input, *indexPath, *manifestPath}); err != nil {
		fmt.Fprintf(os.Stderr, "mediacore verify: %s\n", media.DescribeError(err))
		return 2
	}
	rep, code, err := verify.Run(verify.Input{
		FramesPath:   *input,
		IndexPath:    *indexPath,
		ManifestPath: *manifestPath,
	}, manifest.DefaultVerifyOptions())
	if *asJSON && rep != nil {
		emitJSON(rep)
	} else {
		printVerify(rep)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediacore verify: %s\n", media.DescribeError(err))
	}
	return code
}

func printVerify(rep *verify.Report) {
	if rep == nil {
		return
	}
	fmt.Printf("index_matches_input=%t\n", rep.IndexMatchesInput)
	fmt.Printf("index_complete=%t\n", rep.IndexComplete)
	fmt.Printf("manifest_ok=%t\n", rep.ManifestOK)
	fmt.Printf("recomputed_frames=%d\n", rep.RecomputedFrames)
	if rep.Detail.AlignmentMS > 0 || rep.Detail.TimingChecked {
		fmt.Printf("alignment_error_ms=%d\n", rep.Detail.AlignmentMS)
	}
	for _, c := range rep.Detail.Recomputed {
		if !c.Within {
			fmt.Printf("check_failed name=%s stored=%s recomputed=%s\n", c.Name, c.Stored, c.Recomputed)
		}
	}
	for _, w := range rep.Detail.Warnings {
		fmt.Printf("warning=%s\n", w)
	}
	for _, f := range rep.Failures {
		fmt.Printf("failure field=%s category=%s expected=%s actual=%s %s\n",
			f.Field, f.Category, f.Expected, f.Actual, f.Detail)
	}
	if rep.ManifestOK {
		fmt.Printf("manifest_digest=%s\n", short(rep.ManifestDigest))
		fmt.Println("result=ok")
	}
}

func emitJSON(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediacore: marshal report: %v\n", err)
		return
	}
	fmt.Println(string(b))
}

// checkPaths rejects missing files and directory traversal outside the
// caller's own working set.
func checkPaths(paths []string) error {
	for _, p := range paths {
		clean := filepath.Clean(p)
		if strings.Contains(clean, "..") {
			return media.New(media.CatPathEscape, media.StageCLI, "path %q escapes its directory", p)
		}
		info, err := os.Stat(clean)
		if err != nil {
			return media.Wrap(media.CatIO, media.StageCLI, err, "stat %s", p)
		}
		if info.IsDir() {
			return media.New(media.CatInvalidParams, media.StageCLI, "%s is a directory", p)
		}
	}
	return nil
}

func short(s string) string {
	if len(s) > 16 {
		return s[:16]
	}
	return s
}
