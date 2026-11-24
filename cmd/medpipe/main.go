package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"medpipe/internal/adapter"
	_ "medpipe/internal/adapter/mp4v1"
	"medpipe/internal/clock"
	"medpipe/internal/pipeline"
	"medpipe/internal/sample"
)

type cliFlags struct {
	seed            int64
	packets         int
	loss            float64
	reorder         float64
	duplicate       float64
	queueBytes      int64
	consumerDelay   time.Duration
	checkpointEvery int
	corruptOffset   int64
	resume          bool
	outDir          string
	parserMajor     int
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "demo" {
		fmt.Fprintln(os.Stderr, "usage: medpipe demo [flags]")
		os.Exit(1)
	}
	os.Exit(runDemo(os.Args[2:]))
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f cliFlags
	fs.Int64Var(&f.seed, "seed", 17, "deterministic generator seed")
	fs.IntVar(&f.packets, "packets", 12000, "number of canonical RTP frames")
	fs.Float64Var(&f.loss, "loss", 0.02, "deterministic loss probability")
	fs.Float64Var(&f.reorder, "reorder", 0.08, "deterministic reorder probability")
	fs.Float64Var(&f.duplicate, "duplicate", 0.01, "deterministic duplicate probability")
	fs.Int64Var(&f.queueBytes, "queue-bytes", 1<<20, "hard in-memory queue byte limit")
	fs.DurationVar(&f.consumerDelay, "consumer-delay", 2*time.Millisecond, "simulated downstream processing delay")
	fs.IntVar(&f.checkpointEvery, "checkpoint-every", 500, "write checkpoint every N source chunks")
	fs.Int64Var(&f.corruptOffset, "corrupt-offset", 0, "byte offset to corrupt in generated source")
	fs.BoolVar(&f.resume, "resume", false, "resume from the output checkpoint")
	fs.StringVar(&f.outDir, "out", "./medpipe-output", "local output directory")
	fs.IntVar(&f.parserMajor, "parser-major", pipeline.ParserMajor, "explicitly registered parser major version")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		printCLIError(clock.CodeInvalidPacket, err.Error(), nil)
		return 1
	}
	if f.packets <= 0 || f.queueBytes < 192 || f.checkpointEvery <= 0 {
		printCLIError(clock.CodeInvalidPacket, "packets must be positive, queue-bytes >= 192, checkpoint-every positive", nil)
		return 1
	}
	for name, v := range map[string]float64{"loss": f.loss, "reorder": f.reorder, "duplicate": f.duplicate} {
		if v < 0 || v > 1 {
			printCLIError(clock.CodeInvalidPacket, name+" must be between 0 and 1", nil)
			return 1
		}
	}
	if _, cerr := adapter.Get(f.parserMajor); cerr != nil {
		pipeline.WriteRejection(f.outDir, cerr)
		printClockError(cerr)
		return 1
	}

	opts := pipeline.Options{
		OutDir: f.outDir, QueueBytes: f.queueBytes, ConsumerDelay: f.consumerDelay,
		CheckpointEvery: f.checkpointEvery,
	}
	ctx := context.Background()

	if f.resume {
		res, err := pipeline.Resume(ctx, opts)
		if err != nil {
			var rf *pipeline.ResumeFailure
			if errors.As(err, &rf) {
				printClockError(rf.Reason)
			} else {
				printCLIError(clock.CodeCheckpointInvalid, err.Error(), nil)
			}
			return 1
		}
		printJSON(res)
		return 0
	}

	cfg := sample.DefaultConfig()
	cfg.Seed = f.seed
	cfg.Packets = f.packets
	cfg.Loss = f.loss
	cfg.Reorder = f.reorder
	cfg.Duplicate = f.duplicate
	cfg.CorruptOffset = f.corruptOffset

	runner, generated, err := pipeline.SetupFresh(opts, cfg)
	if err != nil {
		printCLIError(clock.CodeInvalidPacket, err.Error(), nil)
		return 1
	}
	res, err := runner.Run(ctx)
	if err != nil && !errors.Is(err, pipeline.ErrInterrupted) {
		printCLIError(clock.CodeInvalidPacket, err.Error(), nil)
		return 1
	}
	if err != nil {
		printJSON(res)
		return 2
	}
	if generated != nil && generated.Corruption != nil && res.ErrorCode == clock.CodeCorruptPayload {
		res.CorruptByteOffset = generated.Corruption.ActualOffset
		if res.CorruptPacketSeq == 0 {
			res.CorruptPacketSeq = generated.Corruption.PacketSequence
		}
	}
	printJSON(res)
	return res.ExitCode
}

func printJSON(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "{\"error_code\":\"INTERNAL\",\"error\":%q}\n", err.Error())
		return
	}
	fmt.Println(string(b))
}

func printCLIError(code clock.ErrorCode, msg string, loc *clock.Location) {
	printClockError(clock.Errorf(code, loc, "%s", msg))
}

func printClockError(e *clock.Error) {
	b, _ := json.MarshalIndent(e, "", "  ")
	fmt.Fprintln(os.Stderr, string(b))
}
