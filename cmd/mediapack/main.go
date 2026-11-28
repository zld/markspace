// Command mediapack is the acceptance adapter for the reusable media kernel.
// It exposes three terminal subcommands:
//
//	mediapack demo   --out DIR --seed N [--interrupt-after K] [--corrupt MODE]
//	mediapack resume --dir DIR
//	mediapack verify --dir DIR [--repeat N]
//
// Exit codes: 0 success (a demo interruption is a successful checkpointed
// stop), 2 input/structural failure, 3 verification failure.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"mediapack/internal/model"
	"mediapack/internal/pipeline"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "demo":
		err = runDemo(os.Args[2:])
	case "resume":
		err = runResume(os.Args[2:])
	case "verify":
		err = runVerify(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fail(err)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, strings.TrimLeft(`
usage:
  mediapack demo   --out DIR --seed N [--interrupt-after K] [--corrupt mxf-klv-length]
  mediapack resume --dir DIR
  mediapack verify --dir DIR [--repeat N]
`, "\n"))
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	if fe, ok := model.AsField(err); ok {
		if fe.Kind == model.KindChecksum {
			os.Exit(3)
		}
	}
	if pipeline.IsChecksumFailure(err) {
		os.Exit(3)
	}
	os.Exit(2)
}

func emitter() func(pipeline.Event) {
	return func(e pipeline.Event) {
		fmt.Printf("[stage %-8s] %s\n", e.Stage, e.Message)
	}
}

func runDemo(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	out := fs.String("out", "", "output directory")
	seed := fs.Int64("seed", 0, "deterministic sample seed")
	interrupt := fs.Int("interrupt-after", 0, "segments per layer to anchor before interrupting")
	corrupt := fs.String("corrupt", "", "corruption mode: mxf-klv-length")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return model.NewFieldError(model.KindInvalidInput, "", -1, "flags/out",
			"--out is required")
	}
	res, err := pipeline.Run(pipeline.Options{
		OutDir: *out, Seed: *seed, Corrupt: *corrupt,
		InterruptAfter: *interrupt,
	}, emitter())
	if err != nil {
		return err
	}
	printResult(res)
	if res.Interrupted {
		fmt.Println("demo interrupted: checkpoint persisted, run 'resume' to continue")
	}
	return nil
}

func runResume(args []string) error {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	dir := fs.String("dir", "", "output directory containing a checkpoint")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return model.NewFieldError(model.KindInvalidInput, "", -1, "flags/dir",
			"--dir is required")
	}
	res, err := pipeline.Run(pipeline.Options{OutDir: *dir, Resume: true}, emitter())
	if err != nil {
		return err
	}
	printResult(res)
	return nil
}

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	dir := fs.String("dir", "", "output directory to verify")
	repeat := fs.Int("repeat", 1, "number of verification passes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return model.NewFieldError(model.KindInvalidInput, "", -1, "flags/dir",
			"--dir is required")
	}
	vr, err := pipeline.Verify(*dir, *repeat)
	if err != nil {
		return err
	}
	fmt.Printf("verify passes=%d layers=%d frames=%d frame_checks=%d segment_checks=%d\n",
		vr.Passes, vr.LayerCount, vr.FrameCount, vr.FrameChecks, vr.SegmentChecks)
	fmt.Printf("repeat processing differences: %d\n", vr.RepeatDifferences)
	if vr.OK {
		fmt.Println("manifest verification: ok")
		fmt.Printf("manifest sha256: %s\n", vr.ManifestHash)
		return nil
	}
	for _, d := range vr.Differences {
		fmt.Fprintf(os.Stderr, "difference: %s\n", d)
	}
	return model.NewFieldError(model.KindChecksum, *dir, -1, "verify",
		"manifest verification failed")
}

func printResult(r *pipeline.Result) {
	fmt.Printf("stage=%s input_digest=%s\n", r.Stage, r.InputDigest)
	fmt.Printf("duration_ticks=%d max_alignment_error_ticks=%d\n",
		r.DurationTicks, r.MaxAlignTicks)
	total := 0
	for _, l := range r.Layers {
		total += l.Frames
		fmt.Printf("layer=%-4s frames=%d resume_segment=%d resume_frame=%d "+
			"recovered_gaps=%d open_gaps=%d complete=%t\n",
			l.Name, l.Frames, l.LastSegment, l.LastFrame,
			l.RecoveredGaps, l.OpenGaps, l.Complete)
	}
	fmt.Printf("total frames=%d\n", total)
	if r.ManifestHash != "" {
		fmt.Printf("manifest sha256: %s\n", r.ManifestHash)
	}
	for _, a := range r.Audio {
		fmt.Printf("audio track=%d in=%dch/%dHz normalized=%dHz supported=%t\n",
			a.GlobalIndex, a.InputChannels, a.InputSampleRate,
			a.NormalizedRate, a.LayoutSupported)
	}
}
