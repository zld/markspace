// Command regress is the acceptance entry point for the media processing
// kernel. It generates a deterministic fixture (or reads a file), runs the
// five stages, and writes the publication manifest, reporting the counters the
// acceptance check inspects.
//
// Exit codes: 0 manifest verified; 2 input or processing error; 3 manifest
// continuity or digest failure.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/zld/media-regress/internal/error"
	"github.com/zld/media-regress/pipeline"
)

const (
	exitOK       = 0
	exitInput    = 2
	exitManifest = 3
)

func main() {
	os.Exit(run(os.Args[1:]))
}

type flags struct {
	fixture        string
	file           string
	format         string
	sampleRate     int
	layout         string
	loudness       float64
	memoryBytes    int
	maxBlocks      int
	slowConsumerMs int
	corruptBlock   int
	truncateTS     bool
	interruptAfter int
	checkpoint     string
	resume         bool
	cacheDir       string
	tamperCache    bool
	dropSegment    int
	manifestPath   string
}

func parseFlags(args []string) (*flags, error) {
	f := &flags{}
	fs := flag.NewFlagSet("regress", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&f.fixture, "fixture", "valid", "fixture name: valid, corrupt, boundary, gap")
	fs.StringVar(&f.file, "file", "", "read input from a file instead of generating a fixture")
	fs.StringVar(&f.format, "format", "mp4", "fixture container: mp4 or mxf")
	fs.IntVar(&f.sampleRate, "sample-rate", 48000, "target audio sample rate")
	fs.StringVar(&f.layout, "layout", "stereo", "target audio layout")
	fs.Float64Var(&f.loudness, "loudness", -16, "integrated loudness target in LUFS")
	fs.IntVar(&f.memoryBytes, "memory-bytes", 32000, "bounded queue byte limit")
	fs.IntVar(&f.maxBlocks, "max-blocks", 4, "bounded queue block limit")
	fs.IntVar(&f.slowConsumerMs, "slow-consumer-ms", 0, "simulated per-block consumer delay in milliseconds")
	fs.IntVar(&f.corruptBlock, "corrupt-block", 3, "1-based audio block to damage in the corrupt fixture")
	fs.BoolVar(&f.truncateTS, "truncate-timestamp", false, "require the truncated-timestamp boundary fixture")
	fs.IntVar(&f.interruptAfter, "interrupt-after-block", 0, "stop after committing this 1-based block")
	fs.StringVar(&f.checkpoint, "checkpoint", "", "checkpoint file path")
	fs.BoolVar(&f.resume, "resume", false, "resume from the checkpoint")
	fs.StringVar(&f.cacheDir, "cache", "", "content cache directory")
	fs.BoolVar(&f.tamperCache, "tamper-cache", false, "corrupt one cached entry to prove rejection")
	fs.IntVar(&f.dropSegment, "drop-segment", 0, "omit a 1-based manifest segment to simulate a gap")
	fs.StringVar(&f.manifestPath, "manifest", "", "write the manifest JSON to this path")
	if err := fs.Parse(args); err != nil {
		return nil, merr.Wrap(merr.StageFixture, merr.KindParse, 0, 0, err, "invalid arguments")
	}
	if fs.NArg() != 0 {
		return nil, merr.New(merr.StageFixture, merr.KindParse, 0, 0, "unexpected positional arguments: %v", fs.Args())
	}
	return f, nil
}

func run(args []string) int {
	f, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitInput
	}
	data, name, dropSegment, err := loadInput(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitInput
	}
	drop := f.dropSegment
	if drop == 0 {
		drop = dropSegment
	}
	res, err := pipeline.Run(pipeline.Options{
		Source:              name,
		Data:                data,
		SampleRate:          uint32(f.sampleRate),
		Layout:              f.layout,
		Loudness:            f.loudness,
		MaxBlocks:           f.maxBlocks,
		MaxBytes:            f.memoryBytes,
		SlowConsumerMs:      f.slowConsumerMs,
		InterruptAfterBlock: f.interruptAfter,
		Resume:              f.resume,
		CheckpointPath:      f.checkpoint,
		CacheDir:            f.cacheDir,
		TamperCache:         f.tamperCache,
		DropSegment:         drop,
	})
	if err != nil {
		return reportError(res, err)
	}
	return reportSuccess(f, res)
}
