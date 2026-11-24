// Command mediad is the terminal front end for the audio post-production
// pipeline: fixture generation, corruption inspection and processing with
// checkpoint-backed resumption.
package main

import (
	"errors"
	"fmt"
	"os"

	"mediad/internal/checkpoint"
	"mediad/internal/ingest"
	"mediad/internal/pipeline"
)

func main() {
	code := run(os.Args[1:])
	os.Exit(code)
}

// run dispatches subcommands and maps domain errors onto the documented
// exit codes: 0 ok, 2 corrupt input/bad args, 3 checkpoint mismatch,
// 4 internal verification failure.
func run(args []string) int {
	if len(args) == 0 {
		usage()
		return pipeline.ExitCorruptInput
	}
	var err error
	switch args[0] {
	case "process":
		err = cmdProcess(args[1:])
	case "inspect":
		err = cmdInspect(args[1:])
	case "--generate":
		err = cmdGenerate(args[1:])
	case "-h", "--help", "help":
		usage()
		return pipeline.ExitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage()
		return pipeline.ExitCorruptInput
	}
	if err == nil {
		return pipeline.ExitOK
	}
	return mapError(err)
}

func mapError(err error) int {
	var pe *ingest.ParseError
	if errors.As(err, &pe) {
		fmt.Fprintf(os.Stderr, "error_code=%s offset=%d stage=%s\n", pe.Code, pe.Offset, pe.Stage)
		return pipeline.ExitCorruptInput
	}
	var me *checkpoint.MismatchError
	if errors.As(err, &me) {
		reason := "PREFIX"
		if errors.Is(err, checkpoint.ErrConfigMismatch) {
			reason = "CONFIG"
		}
		fmt.Fprintf(os.Stderr, "error_code=CHECKPOINT_%s_MISMATCH offset=%d stage=resume\n",
			reason, me.Offset)
		return pipeline.ExitCheckpointBad
	}
	if errors.Is(err, pipeline.ErrInterrupted) {
		return pipeline.ExitOK // an intentional interrupt is not a failure
	}
	// Argument and input-file errors share the corrupt-input/bad-args code.
	if errors.Is(err, errBadArgs) || os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "error_code=BAD_ARGS stage=args: %v\n", err)
		return pipeline.ExitCorruptInput
	}
	fmt.Fprintf(os.Stderr, "error_code=INTERNAL stage=verify: %v\n", err)
	return pipeline.ExitInternalVerify
}

var errBadArgs = errors.New("bad arguments")

func badArgs(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errBadArgs, fmt.Sprintf(format, a...))
}

func usage() {
	fmt.Fprint(os.Stderr, `mediad - audio post-production pipeline

  mediad --generate <demo|corrupt> --out <file>
  mediad inspect  --in <file>
  mediad process  --in <file> --out <pcm> [flags]

process flags:
  --rate <hz>            target sample rate (default 48000)
  --layout <mono|stereo> target channel layout (default stereo)
  --target-lufs <db>     integrated loudness target (default -16)
  --checkpoint <file>    checkpoint path enabling resume
  --interrupt-after <n>  stop after n RTP packets at a block boundary
`)
}
