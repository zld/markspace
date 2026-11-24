// Command archivectl is the terminal acceptance entry point for the archive
// recovery pipeline. All fixtures are generated locally from a seed; no
// external media files or services are used.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"archive-recovery/internal/media"
	"archive-recovery/internal/testkit"
)

const usageText = `archivectl - historical bitstream recovery utility

usage:
  archivectl demo   --codec <h264|h265|av1> --seed <int> --chunk-bytes <int>
                    [--interrupt-after <n>] [--resume] [--scenario <name>] [--workdir <dir>]
  archivectl verify --codec <h264|h265|av1> --seed <int> [--chunk-bytes <int>]
                    [--workdir <dir>]

exit codes: 0 ok, 2 invalid input/arguments, 3 recoverable parse error,
             4 checkpoint or verification failure
`

type cliOptions struct {
	command        string
	codec          string
	seed           int64
	chunkBytes     int64
	interruptAfter int
	resume         bool
	scenario       string
	workdir        string
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usageText)
		return 0
	}
	opts, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "archivectl: %v\n", err)
		return media.ClassInvalidArgument.ExitCode()
	}
	if err := os.MkdirAll(opts.workdir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "archivectl: workdir: %v\n", err)
		return media.ClassCheckpoint.ExitCode()
	}

	switch opts.command {
	case "demo":
		return runDemo(opts)
	case "verify":
		return runVerify(opts)
	default:
		fmt.Fprintf(os.Stderr, "archivectl: unknown command %q\n", opts.command)
		return media.ClassInvalidArgument.ExitCode()
	}
}

func parseArgs(args []string) (cliOptions, error) {
	o := cliOptions{
		command:    args[0],
		chunkBytes: 4096,
		scenario:   testkit.ScenarioNormal,
	}
	seen := map[string]bool{}
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			return o, fmt.Errorf("unexpected positional argument %q", arg)
		}
		key := strings.TrimPrefix(arg, "--")
		needValue := key != "resume"
		var value string
		if needValue {
			if i+1 >= len(args) {
				return o, fmt.Errorf("flag --%s requires a value", key)
			}
			value = args[i+1]
			i++
		}
		seen[key] = true
		switch key {
		case "codec":
			o.codec = value
		case "seed":
			v, err := strconv.ParseInt(value, 10, 64)
			if err != nil || v < 0 {
				return o, fmt.Errorf("seed must be a non-negative integer")
			}
			o.seed = v
		case "chunk-bytes":
			v, err := strconv.ParseInt(value, 10, 64)
			if err != nil || v <= 0 {
				return o, fmt.Errorf("chunk-bytes must be a positive integer")
			}
			o.chunkBytes = v
		case "interrupt-after":
			v, err := strconv.Atoi(value)
			if err != nil || v < 0 {
				return o, fmt.Errorf("interrupt-after must be a non-negative integer")
			}
			o.interruptAfter = v
		case "scenario":
			o.scenario = value
		case "workdir":
			o.workdir = value
		case "resume":
			o.resume = true
		default:
			return o, fmt.Errorf("unknown flag --%s", key)
		}
	}
	if o.codec == "" {
		return o, fmt.Errorf("--codec is required")
	}
	if !seen["seed"] {
		return o, fmt.Errorf("--seed is required")
	}
	if o.workdir == "" {
		o.workdir = filepath.Join(os.TempDir(),
			fmt.Sprintf("archivectl-%s-%s-%d", o.command, o.codec, o.seed))
	}
	return o, nil
}
