// Command mediatool drives the CMAF/fMP4 pipeline from the terminal:
//
//	mediatool demo   --seed N --fragments N --late-at N --out DIR
//	mediatool verify --dir DIR
//
// Exit codes: 0 success, 1 input/processing failure, 2 argument error.
package main

import (
	"fmt"
	"os"
)

const usage = `mediatool - deterministic CMAF/fMP4 remux pipeline

usage:
  mediatool demo   --seed <int> --fragments <int> --late-at <int> --out <dir>
  mediatool verify --dir <dir>

flags:
  --seed       deterministic RNG seed (default 42)
  --fragments  fragments per track (default 12)
  --late-at    input order moved behind the watermark, -1 disables (default 7)
  --out        local output directory (demo only)
  --dir        existing output directory to verify
`

// exitCode is the process status; tests read it via run().
var exitCode int

func main() {
	os.Exit(run(os.Args[1:]))
}

// run dispatches the subcommand and returns the process exit code.
func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n%s", args[0], usage)
		return 2
	}
}

// flagInt extracts an integer flag from args, reporting an argument error.
func flagInt(args []string, name string, def int) (int, []string, error) {
	return parseFlag(args, name, def, parseInt)
}

// flagString extracts a string flag from args.
func flagString(args []string, name, def string) (string, []string, error) {
	return parseFlag(args, name, def, func(s string) (string, error) { return s, nil })
}

func parseFlag[T any](args []string, name string, def T,
	convert func(string) (T, error)) (T, []string, error) {
	var out T = def
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] != name {
			rest = append(rest, args[i])
			continue
		}
		if i+1 >= len(args) {
			return out, rest, fmt.Errorf("flag %s requires a value", name)
		}
		v, err := convert(args[i+1])
		if err != nil {
			return out, rest, fmt.Errorf("flag %s: %w", name, err)
		}
		out = v
		i++
	}
	return out, rest, nil
}

func parseInt(s string) (int, error) {
	var v int
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil {
		return 0, fmt.Errorf("not an integer: %q", s)
	}
	return v, nil
}
