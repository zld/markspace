// Command mediasync is the acceptance entry point for the media processing
// library. It generates a deterministic PCM/RTP fixture with the requested
// fault profile, runs the full repair/recovery/index pipeline, prints the
// summary JSON and exits with the documented status code.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"mediasync/internal/clock"
	"mediasync/internal/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("mediasync", flag.ContinueOnError)
	seed := fs.Int64("seed", 7, "deterministic fixture seed")
	duration := fs.Duration("duration", 30*time.Second, "generated stream duration (e.g. 30s)")
	lossBurst := fs.Int("loss-burst", 0, "size of the injected loss burst (packets)")
	reorder := fs.Int("reorder", 0, "number of adjacent packet reorder swaps")
	truncate := fs.Int("truncate-audio", 0, "bytes removed from the WAV tail")
	corrupt := fs.Int("corrupt-index", 0, "number of corrupt index records injected")
	retry := fs.Int("retry-limit", 2, "per-node retries after the first attempt")
	journal := fs.String("journal", "", "optional checkpoint journal path")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	opt := pipeline.Options{
		Seed: *seed, Duration: *duration, LossBurst: *lossBurst,
		Reorder: *reorder, TruncateAudio: *truncate, CorruptIndex: *corrupt,
		RetryLimit: *retry, JournalPath: *journal,
	}
	res, err := pipeline.Run(opt)
	if err != nil {
		// Parameter / validation errors are exit code 1 per the contract.
		var me *clock.MediaError
		code := 2
		if asMedia(err, &me) && me.Code == clock.ErrInvalidArgument {
			code = 1
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return code
	}

	raw, err := json.MarshalIndent(res.Summary, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	fmt.Println(string(raw))
	return res.Summary.ExitCode
}

func asMedia(err error, target **clock.MediaError) bool {
	for err != nil {
		if me, ok := err.(*clock.MediaError); ok {
			*target = me
			return true
		}
		type wrapper interface{ Unwrap() error }
		w, ok := err.(wrapper)
		if !ok {
			return false
		}
		err = w.Unwrap()
	}
	return false
}
