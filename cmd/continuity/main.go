// Command continuity is the terminal acceptance adapter. It generates
// synthetic stereo PCM/WAV and a fault-injected RTP stream, rebuilds the
// sample timeline, updates the incremental index and prints quantitative
// evidence. All artifacts stay inside the requested work directory.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"continuity/internal/demo"
	"continuity/internal/index"
	"continuity/internal/media"
	mp4box "continuity/internal/mp4"
	"continuity/internal/pipeline"
	"continuity/internal/wav"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error category=%s: %v\n", media.CategoryOf(err), err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return media.NewError(media.CatInvalidInput, "missing command")
	}
	switch args[0] {
	case "demo":
		return demoCmd(args[1:])
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return media.NewError(media.CatInvalidInput, "unknown command: "+args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: continuity demo --work DIR [--seed N] [--corrupt MODE] [--container-change]
modes: truncated-wav | checkpoint
`)
}

type options struct {
	work            string
	seed            int64
	corrupt         string
	containerChange bool
}

func parseFlags(fs *flag.FlagSet, args []string) (options, error) {
	var o options
	fs.StringVar(&o.work, "work", "", "working directory for index and artifacts")
	fs.Int64Var(&o.seed, "seed", 17, "deterministic generator seed")
	fs.StringVar(&o.corrupt, "corrupt", "", "fault injection: truncated-wav | checkpoint")
	fs.BoolVar(&o.containerChange, "container-change", false, "change only the MP4 container metadata")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.work == "" {
		return o, media.NewError(media.CatInvalidInput, "--work is required")
	}
	switch o.corrupt {
	case "", "truncated-wav", "checkpoint":
	default:
		return o, media.NewError(media.CatInvalidInput, "unknown --corrupt mode: "+o.corrupt)
	}
	return o, nil
}

func demoCmd(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	o, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(o.work, 0o755); err != nil {
		return media.WrapError(media.CatIO, "create work dir", err)
	}

	// 1. Generate and persist the synthetic WAV fixture.
	pcm := demo.PCM(o.seed)
	wavPath := filepath.Join(o.work, "source.wav")
	if err := writeWAV(wavPath, pcm); err != nil {
		return err
	}
	if o.corrupt == "truncated-wav" {
		return exerciseTruncatedWAV(o.work, wavPath)
	}

	// 2. Generate the fault-injected RTP arrival stream and rebuild it.
	arrival := demo.Generate(o.seed, demo.DefaultPlan())
	blocks, stats, alignErr, err := demo.Rebuild(arrival)
	if err != nil {
		return err
	}

	// 3. Inspect the (synthetic) container metadata. A first clean run
	// records it; on repeat runs it is identical and only reused.
	containerMeta, containerDigest, err := inspectContainer(o.work, o.seed, o.containerChange)
	if err != nil {
		return err
	}

	// 4. Drive the incremental pipeline.
	layout, err := wav.NewLayout(demo.Channels, demo.Bits, demo.SampleRate)
	if err != nil {
		return err
	}
	inputs := make([]pipeline.BlockInput, 0, len(blocks))
	for _, b := range blocks {
		inputs = append(inputs, pipeline.BlockInput{
			Block:            b,
			Layout:           layout,
			AlignmentSamples: alignErr,
		})
	}
	cfg := pipeline.Config{
		WorkDir:         filepath.Join(o.work, "pipeline"),
		BlockFrames:     demo.BlockFrames,
		FramesPerBlock:  4,
		FrameWidth:      16,
		FrameHeight:     16,
		ContainerDigest: containerDigest,
		ContainerMeta:   containerMeta,
	}
	if o.corrupt == "checkpoint" {
		if err := corruptCheckpoint(cfg.WorkDir); err != nil {
			return err
		}
	}
	runner, err := pipeline.New(cfg)
	if err != nil {
		return err
	}
	m, err := runner.Run(inputs, stats)
	if err != nil {
		return err
	}
	printReport(o, m, alignErr, runner)
	return nil
}

func writeWAV(path string, pcm []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return media.WrapError(media.CatIO, "create wav", err)
	}
	defer f.Close()
	return wav.WritePCM(f, demo.SampleRate, demo.Channels, demo.Bits, pcm)
}

// exerciseTruncatedWAV verifies that a truncated header is rejected and
// that no index block is added. It returns a categorized error so the
// process exits non-zero with a printed category.
func exerciseTruncatedWAV(work, wavPath string) error {
	raw, err := os.ReadFile(wavPath)
	if err != nil {
		return media.WrapError(media.CatIO, "read wav", err)
	}
	truncPath := filepath.Join(work, "truncated.wav")
	if err := os.WriteFile(truncPath, raw[:20], 0o644); err != nil {
		return media.WrapError(media.CatIO, "write truncated wav", err)
	}
	f, err := os.Open(truncPath)
	if err != nil {
		return media.WrapError(media.CatIO, "open truncated wav", err)
	}
	defer f.Close()
	store, _ := index.OpenStore(filepath.Join(work, "pipeline", "index"))
	before := store.BlockCount()
	if _, perr := wav.Parse(f); perr == nil {
		return media.NewError(media.CatUnrecoverable, "truncated WAV was unexpectedly accepted")
	} else {
		after := store.BlockCount()
		fmt.Printf("truncated_wav_rejected=true category=%s index_blocks_before=%d after=%d\n",
			media.CategoryOf(perr), before, after)
		if after != before {
			return media.NewError(media.CatState, "index blocks increased on corrupt input")
		}
		return perr
	}
}

func inspectContainer(work string, seed int64, changed bool) (index.ContainerRecord, string, error) {
	brand := "isom"
	minor := uint32(1)
	if changed {
		brand = "mp42"
		minor = 2
	}
	md := syntheticContainer(brand, minor, seed)
	digest := mp4box.Digest(md)
	return index.ContainerRecord{
		Hash:         digest,
		Timescale:    md.Timescale,
		Duration:     md.Duration,
		Brand:        md.Brand,
		BoxSignature: strings.Join(md.BoxSignature, ","),
	}, digest, nil
}

func printReport(o options, m pipeline.Manifest, alignErr int64, runner *pipeline.Runner) {
	fmt.Printf("blocks=%d\n", m.Blocks)
	fmt.Printf("duplicates=%d\n", m.Duplicates)
	fmt.Printf("reordered=%d\n", m.Reordered)
	fmt.Printf("gaps=%d\n", m.Gaps)
	fmt.Printf("pcm_hash=%s\n", index.ShortHash(m.PCMHash))
	fmt.Printf("segment_hash=%s\n", index.ShortHash(m.SegmentHash))
	fmt.Printf("pixel_mean=%.2f\n", m.PixelMean)
	fmt.Printf("peak_dbfs=%.2f\n", m.PeakDBFS)
	fmt.Printf("alignment_error_samples=%d\n", alignErr)
	fmt.Printf("reused=%d\n", m.Reused)
	fmt.Printf("recomputed=%d\n", m.Recomputed)
	fmt.Printf("pcm_recomputed=%d container_recomputed=%d\n", m.PCMRecomputed, m.ContainerRecomputed)
	if rb, ok := runner.Store().Rollback(); ok {
		fmt.Printf("checkpoint_rollback_blocks=%d reason=%q\n", rb.Dropped, rb.Reason)
	}
}
