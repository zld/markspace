// Command mediasync is the acceptance CLI for the offline media-sync kernel.
// It is a thin shell over the reusable internal/pipeline library:
//
//	mediasync demo   --seed N --out DIR [--case truncated]
//	mediasync verify --dir DIR
//	mediasync recover --dir DIR
//
// Exit codes: 0 success, 1 input/processing failure, 2 argument error.
package main

import (
	"fmt"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage()
		return 2
	}
	switch args[0] {
	case "demo":
		return cmdDemo(args[1:])
	case "verify":
		return cmdVerify(args[1:])
	case "recover":
		return cmdRecover(args[1:])
	case "-h", "--help", "help":
		printUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		printUsage()
		return 2
	}
}

func printUsage() {
	fmt.Fprint(os.Stderr, `mediasync - offline multilingual media sync kernel

usage:
  mediasync demo   --seed <int> --out <dir> [--case truncated]
  mediasync verify --dir <dir>
  mediasync recover --dir <dir>

exit codes: 0 ok, 1 input/processing failure, 2 argument error
`)
}

// flags parses --key value pairs from args. A flag present without a value,
// or an unknown flag, yields an argument error.
func parseFlags(args []string, want map[string]bool) (map[string]string, int) {
	out := map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) < 3 || a[:2] != "--" {
			fmt.Fprintf(os.Stderr, "unexpected argument %q\n", a)
			return nil, 2
		}
		key := a[2:]
		if !want[key] {
			fmt.Fprintf(os.Stderr, "unknown flag --%s\n", key)
			return nil, 2
		}
		if i+1 >= len(args) {
			fmt.Fprintf(os.Stderr, "flag --%s requires a value\n", key)
			return nil, 2
		}
		i++
		out[key] = args[i]
	}
	return out, 0
}
