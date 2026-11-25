// Command mediad is the terminal acceptance entry point for the local media
// processing kernel. It generates self-contained fixtures, runs the full
// pipeline and prints verifiable key=value results.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	errs "mediad/internal/errors"
	"mediad/internal/fixture"
	"mediad/internal/pipeline"
	"mediad/internal/stream"
)

// Exit codes mandated by the acceptance contract.
const (
	exitOK             = 0
	exitStructural     = 2
	exitCheckpointDiff = 3
	exitInternal       = 4
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitInternal
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "resume":
		return runResume(args[1:])
	case "-h", "--help", "help":
		usage()
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage()
		return exitInternal
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `mediad - local media processing kernel
usage:
  mediad demo   --codec h264|hevc|av1 --frames N --index exact|shifted
                [--truncate K] [--interrupt-after N] [--workdir DIR]
  mediad resume --fixture <id-or-codec> [--workdir DIR]
exit codes: 0 completed | 2 structural damage (partial manifest) |
             3 checkpoint/input mismatch | 4 internal or write error`)
}

func demoFlags(fs *flag.FlagSet) (codec, indexMode *string, frames, truncate, interrupt *int, workdir *string) {
	codec = fs.String("codec", "h264", "h264, hevc or av1")
	frames = fs.Int("frames", 12, "number of frame access units")
	indexMode = fs.String("index", fixture.IndexExact, "exact or shifted")
	truncate = fs.Int("truncate", 0, "keep K full frames then append a partial corrupt AU")
	interrupt = fs.Int("interrupt-after", 0, "simulate interruption after N committed segments")
	workdir = fs.String("workdir", ".mediad-work", "state directory root")
	return
}

func fixtureID(codec string, indexMode string, truncate int) string {
	id := codec
	if indexMode != fixture.IndexExact {
		id += "-" + indexMode
	}
	if truncate > 0 {
		id += fmt.Sprintf("-t%d", truncate)
	}
	return id
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	codecP, indexP, framesP, truncP, intP, workP := demoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return exitInternal
	}
	codec, err := stream.ParseCodec(*codecP)
	if err != nil {
		return fail(err)
	}
	spec := fixture.Spec{
		Codec: codec, Frames: *framesP, IndexMode: *indexP, Truncate: *truncP,
	}
	fx, err := fixture.Generate(spec)
	if err != nil {
		return fail(err)
	}
	id := fixtureID(string(codec), *indexP, *truncP)
	dir := filepath.Join(*workP, id)
	if err := writeSpec(dir, persistedSpec{
		FixtureID: id, Codec: codec, Frames: *framesP,
		IndexMode: *indexP, Truncate: *truncP,
	}); err != nil {
		return fail(err)
	}

	res, err := pipeline.Run(context.Background(), pipeline.Options{
		FixtureID: id, Codec: codec, Stream: fx.Stream, IndexText: fx.IndexText,
		WorkDir: dir, QueueSize: 4, InterruptAfter: *intP,
		StreamCfg: stream.DefaultConfig(),
	})
	if err != nil {
		return fail(err)
	}
	printResult(res)
	if res.Status == pipeline.StatusInterrupted {
		return exitOK // controlled, checkpointed stop; not a structural failure
	}
	if res.CorruptSegmentCount > 0 || res.KeyframeHitRate < 1.0 {
		return exitStructural
	}
	return exitOK
}

func runResume(args []string) int {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fxID := fs.String("fixture", "", "fixture id or codec prefix recorded by demo")
	workP := fs.String("workdir", ".mediad-work", "state directory root")
	intP := fs.Int("interrupt-after", 0, "accepted for command-line parity; a resume always runs to completion")
	if err := fs.Parse(args); err != nil {
		return exitInternal
	}
	_ = intP
	if *fxID == "" {
		fmt.Fprintln(os.Stderr, "resume requires --fixture")
		return exitInternal
	}
	dir, err := resolveDir(*workP, *fxID)
	if err != nil {
		return fail(err)
	}
	spec, err := readSpec(dir)
	if err != nil {
		return fail(err)
	}
	fx, err := fixture.Generate(fixture.Spec{
		Codec: spec.Codec, Frames: spec.Frames, IndexMode: spec.IndexMode,
		Truncate: spec.Truncate, PayloadBytes: spec.PayloadBytes,
	})
	if err != nil {
		return fail(err)
	}
	res, err := pipeline.Run(context.Background(), pipeline.Options{
		FixtureID: spec.FixtureID, Codec: spec.Codec,
		Stream: fx.Stream, IndexText: fx.IndexText,
		WorkDir: dir, QueueSize: 4, StreamCfg: stream.DefaultConfig(),
	})
	if err != nil {
		return fail(err)
	}
	printResult(res)
	fmt.Printf("resumed_from=%d\n", res.ResumedFrom)

	// Determinism proof: a fresh, checkpoint-free run of the same input must
	// produce a manifest with the identical canonical hash.
	tmp, err := os.MkdirTemp(*workP, "verify-*")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(tmp)
	verify, err := pipeline.Run(context.Background(), pipeline.Options{
		FixtureID: spec.FixtureID, Codec: spec.Codec,
		Stream: fx.Stream, IndexText: fx.IndexText,
		WorkDir: tmp, QueueSize: 4, StreamCfg: stream.DefaultConfig(),
	})
	if err != nil {
		return fail(err)
	}
	match := verify.ManifestCanonicalHash == res.ManifestCanonicalHash
	fmt.Printf("manifest_canonical_hash=%s\n", res.ManifestCanonicalHash)
	fmt.Printf("repeated_manifest_canonical_hash=%s\n", verify.ManifestCanonicalHash)
	fmt.Printf("manifest_hash_match=%t\n", match)
	if !match {
		return exitInternal
	}
	if res.CorruptSegmentCount > 0 || res.KeyframeHitRate < 1.0 {
		return exitStructural
	}
	return exitOK
}

func printResult(res *pipeline.Result) {
	fmt.Printf("status=%s\n", res.Status)
	fmt.Printf("segments=%d\n", len(res.Segments))
	fmt.Printf("roundtrip_rate=%.3f\n", res.RoundTripRate)
	fmt.Printf("keyframe_hit_rate=%.3f\n", res.KeyframeHitRate)
	fmt.Printf("corrupt_segments=%d\n", res.CorruptSegmentCount)
	if res.Match != nil {
		fmt.Printf("index_hits=%d index_misses=%d\n", res.Match.Hits, res.Match.Misses)
	}
	if len(res.Damages) > 0 {
		offsets := make([]int64, 0, len(res.Damages))
		for _, d := range res.Damages {
			offsets = append(offsets, d.Offset)
		}
		fmt.Printf("damaged_offsets=%v\n", offsets)
	}
	for _, s := range res.Segments {
		flag := ""
		if s.Corrupt {
			flag = " corrupt=true"
		}
		fmt.Printf("segment[%d] sha256=%s%s\n", s.ID, s.Hash, flag)
	}
}

// fail maps a uniform-model error onto the mandated exit code.
func fail(err error) int {
	e := errs.As(err)
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	switch e.Code {
	case errs.CodeCheckpointMismatch:
		return exitCheckpointDiff
	case errs.CodeIOFailure, errs.CodeInternal:
		return exitInternal
	default:
		return exitStructural
	}
}
