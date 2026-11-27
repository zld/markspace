// continuity recovers a continuous program timeline from Matroska/WebM
// material and RTP packets, with checkpoints and an auditable manifest.
//
// Usage:
//
//	continuity demo   --seed N --workers W --queue Q --checkpoint P --manifest M [--case C [N]]
//	continuity resume --seed N --workers W --queue Q --checkpoint P --manifest M
//
// Exit codes: 0 completed, 2 corrupt input or invalid arguments,
// 3 cancelled or internal error.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	"continuity/internal/pipeline"
)

func main() { os.Exit(run(os.Args)) }

func run(args []string) int {
	if len(args) < 2 || (args[1] != "demo" && args[1] != "resume") {
		fmt.Fprintln(os.Stderr, "usage: continuity <demo|resume> [--seed N] [--workers W] [--queue Q] [--checkpoint P] [--manifest M] [--case C [N]]")
		return pipeline.ExitInput
	}
	cmd := args[1]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	seed := fs.Int64("seed", 1, "deterministic fixture seed")
	workers := fs.Int("workers", 4, "worker count (1..64)")
	queue := fs.Int("queue", 8, "queue capacity (1..4096)")
	checkpoint := fs.String("checkpoint", "/tmp/continuity.state", "checkpoint file path")
	manifestPath := fs.String("manifest", "/tmp/continuity.json", "manifest file path")
	kase := fs.String("case", "", "fault case: truncated-block|unknown-length-boundary|cancel-after")
	if err := fs.Parse(args[2:]); err != nil {
		return pipeline.ExitInput
	}
	cancelAfter := 0
	if *kase == "cancel-after" {
		cancelAfter = 5
		if rest := fs.Args(); len(rest) > 0 {
			n, err := strconv.Atoi(rest[0])
			if err != nil || n < 1 {
				fmt.Fprintln(os.Stderr, "invalid cancel-after count")
				return pipeline.ExitInput
			}
			cancelAfter = n
		}
	}
	res := pipeline.Run(pipeline.Options{
		Seed:           *seed,
		Workers:        *workers,
		Queue:          *queue,
		Case:           *kase,
		CancelAfter:    cancelAfter,
		CheckpointPath: *checkpoint,
		ManifestPath:   *manifestPath,
		Resume:         cmd == "resume",
	}, func(line string) { fmt.Println(line) })
	return res.ExitCode
}
