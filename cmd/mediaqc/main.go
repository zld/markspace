// Command mediaqc is the terminal front-end of the local media QC
// library: generate fixtures, process masters (scan -> repair -> stream
// -> remux) and run the selfcheck suite. Exit codes: 0 ok, 2 corrupt
// input or mapping error, 3 recoverable interrupt, 1 internal error.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"mediaqc/internal/qcrun"
	"mediaqc/internal/samples"
)

func main() {
	os.Exit(dispatch(os.Args[1:]))
}

func dispatch(args []string) int {
	if len(args) == 0 {
		usage()
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "generate":
		return cmdGenerate(args[1:])
	case "process":
		return cmdProcess(ctx, args[1:])
	case "selfcheck":
		return cmdSelfcheck(ctx, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", args[0])
		usage()
		return 1
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  mediaqc generate --dir <dir>
  mediaqc process --input <file> --output <file> [--queue N] [--consumer-delay D]
                  [--resume] [--map 1=1,2=2] [--max-skew-ms F]
  mediaqc selfcheck --dir <dir>`)
}

func cmdGenerate(args []string) int {
	var dir string
	for i := 0; i < len(args); i++ {
		if args[i] == "--dir" && i+1 < len(args) {
			dir = args[i+1]
			i++
		}
	}
	if dir == "" {
		fmt.Fprintln(os.Stderr, "generate: --dir is required")
		return 1
	}
	if err := samples.WriteAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "generate: %v\n", err)
		return 1
	}
	out, _ := json.Marshal(map[string]any{
		"dir": dir, "samples": samples.Names(), "ok": true,
	})
	fmt.Println(string(out))
	return 0
}

func cmdSelfcheck(ctx context.Context, args []string) int {
	var dir string
	for i := 0; i < len(args); i++ {
		if args[i] == "--dir" && i+1 < len(args) {
			dir = args[i+1]
			i++
		}
	}
	if dir == "" {
		fmt.Fprintln(os.Stderr, "selfcheck: --dir is required")
		return 1
	}
	rep := qcrun.Selfcheck(ctx, dir)
	out, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(out))
	if rep.OK {
		return 0
	}
	return 1
}
