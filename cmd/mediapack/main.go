// mediapack is the demo and acceptance entry point of the media pipeline.
//
// Exit codes: 0 = all checks passed, 2 = invalid input or arguments,
// 3 = unrecoverable fragment encountered, 4 = index write/verify failure.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediapack/internal/batch"
	"mediapack/internal/clock"
	"mediapack/internal/fixture"
)

const (
	exitOK            = 0
	exitUsage         = 2
	exitUnrecoverable = 3
	exitIndex         = 4
)

func main() { os.Exit(run(os.Args[1:])) }

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  mediapack demo --work DIR [--tracks N] [--fragments M] [--chunk K] [--inject a,b,c]
  mediapack run  --work DIR [--chunk K]`)
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitUsage
	}
	switch args[0] {
	case "demo":
		return demo(args[1:])
	case "run":
		return runOnly(args[1:])
	default:
		usage()
		return exitUsage
	}
}

// exitForError maps a pipeline error to an exit code.
func exitForError(err error) int {
	switch {
	case clock.HasCategory(err, clock.CatIndex):
		return exitIndex
	case clock.HasCategory(err, clock.CatUnrecoverable):
		return exitUnrecoverable
	case clock.HasCategory(err, clock.CatIO), clock.HasCategory(err, clock.CatParse):
		return exitUsage
	default:
		return exitIndex
	}
}

func runOnly(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	work := fs.String("work", "", "work directory")
	chunk := fs.Int("chunk", 8, "records per index chunk")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *work == "" || *chunk < 1 {
		usage()
		return exitUsage
	}
	rep, err := batch.Run(batch.Options{WorkDir: *work, ChunkSize: *chunk})
	if err != nil {
		fmt.Fprintln(os.Stderr, "run failed:", err)
		return exitForError(err)
	}
	printReport(rep)
	if rep.Unrecoverable > 0 {
		return exitUnrecoverable
	}
	return exitOK
}

func demo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	work := fs.String("work", "", "work directory")
	tracks := fs.Int("tracks", 4, "number of tracks")
	fragments := fs.Int("fragments", 32, "total fragments across tracks")
	chunk := fs.Int("chunk", 8, "records per index chunk")
	inject := fs.String("inject", "", "comma-separated fault names")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *work == "" || *tracks < 1 || *fragments < *tracks || *chunk < 1 {
		fmt.Fprintln(os.Stderr, "invalid arguments")
		usage()
		return exitUsage
	}
	var injects []string
	if *inject != "" {
		injects = strings.Split(*inject, ",")
	}

	// Fresh, deterministic input inside the work directory.
	if err := os.RemoveAll(*work); err != nil {
		fmt.Fprintln(os.Stderr, "clean work dir:", err)
		return exitUsage
	}
	cfg := fixture.Config{
		Dir: filepath.Join(*work, "input"),
		Tracks: *tracks, Fragments: *fragments, Inject: injects,
	}
	if err := fixture.Generate(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "generate fixtures:", err)
		return exitUsage
	}
	if hasInject(injects, fixture.FaultPartialIndex) {
		if err := fixture.PlantPartialIndex(*work); err != nil {
			fmt.Fprintln(os.Stderr, "plant partial index:", err)
			return exitIndex
		}
	}

	opts := batch.Options{WorkDir: *work, ChunkSize: *chunk}
	rep1, err := batch.Run(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run failed:", err)
		return exitForError(err)
	}
	// Second run on the same directory reuses validated chunks.
	rep2, err := batch.Run(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "resume run failed:", err)
		return exitForError(err)
	}
	// Third run on a fresh copy proves input-only determinism.
	fresh := filepath.Join(*work, "rerun")
	cfg.Dir = filepath.Join(fresh, "input")
	if err := fixture.Generate(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "regenerate fixtures:", err)
		return exitUsage
	}
	rep3, err := batch.Run(batch.Options{WorkDir: fresh, ChunkSize: *chunk})
	if err != nil {
		fmt.Fprintln(os.Stderr, "fresh run failed:", err)
		return exitForError(err)
	}

	v := &batch.Validation{}
	if err := batch.ValidateRoundtrip(opts, rep1, v); err != nil {
		fmt.Fprintln(os.Stderr, "roundtrip validation:", err)
		return exitIndex
	}
	batch.ValidateKeyframes(rep1, v)
	matchResume, total := batch.HashMatch(rep1, rep2)
	matchFresh, _ := batch.HashMatch(rep1, rep3)
	match := matchResume
	if matchFresh < match {
		match = matchFresh
	}

	invariants := "PASS"
	exitCode := exitOK
	switch {
	case rep1.Unrecoverable > 0:
		exitCode = exitUnrecoverable
	case match != total || v.RoundtripRate != 1.0 || v.KeyframeHitRate != 1.0:
		invariants = "FAIL"
		exitCode = exitIndex
	}

	printReport(rep1)
	fmt.Printf("roundtrip_rate=%.3f\n", v.RoundtripRate)
	fmt.Printf("keyframe_hit_rate=%.3f\n", v.KeyframeHitRate)
	fmt.Printf("fragment_hash_match=%d/%d\n", match, total)
	fmt.Printf("recovered_chunks=%d\n", rep1.RecoveredChunks)
	fmt.Printf("rejected_fragments=%d\n", rep1.Rejected)
	fmt.Printf("invariants=%s\n", invariants)
	return exitCode
}

func hasInject(injects []string, name string) bool {
	for _, s := range injects {
		if s == name {
			return true
		}
	}
	return false
}

func printReport(rep *batch.Report) {
	fmt.Printf("input_hash=%s\n", rep.InputHash)
	fmt.Printf("accepted=%d rejected=%d repaired=%d quarantined=%d duplicates=%d unrecoverable=%d\n",
		rep.Accepted, rep.Rejected, rep.Repaired, rep.Quarantined, rep.Duplicates, rep.Unrecoverable)
	fmt.Printf("reused_chunks=%d warnings=%d\n", rep.ReusedChunks, len(rep.Warnings))
	for _, w := range rep.Warnings {
		fmt.Println("warning:", w)
	}
}
