// Command mediapack is the demonstration and acceptance adapter for the
// reusable media-pipeline library. It generates its own inputs and faults,
// runs the scanner -> index -> timeline -> manifest/ladder -> runner flow,
// and prints machine-checkable key=value lines. It contains no media decoding.
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Exit codes follow the acceptance contract.
const (
	exitOK        = 0
	exitDataFault = 2  // corrupt input or unalignable
	exitUsage     = 64 // bad command-line arguments
)

type demoArgs struct {
	work    string
	tracks  int
	ladders int
	segment time.Duration
	faults  string
	resume  bool
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitUsage)
	}
	switch os.Args[1] {
	case "demo":
		code := runDemo(os.Args[2:])
		os.Exit(code)
	case "check":
		code := runCheck(os.Args[2:])
		os.Exit(code)
	case "-h", "--help", "help":
		usage()
		os.Exit(exitOK)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(exitUsage)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr,
		"usage:\n"+
			"  mediapack demo  --work <dir> --tracks <n> --ladders <n> --segment <dur> --faults <all|none> --resume\n"+
			"  mediapack check --work <dir> [--truncate-box|--truncate-mxf]")
}

func parseDemoArgs(argv []string) (demoArgs, error) {
	a := demoArgs{tracks: 1, ladders: 1, segment: 2 * time.Second, faults: "none"}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		need := func() (string, error) {
			if i+1 >= len(argv) {
				return "", fmt.Errorf("flag %s needs a value", arg)
			}
			i++
			return argv[i], nil
		}
		switch arg {
		case "--work":
			v, err := need()
			if err != nil {
				return a, err
			}
			a.work = v
		case "--tracks":
			v, err := need()
			if err != nil {
				return a, err
			}
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return a, fmt.Errorf("--tracks must be a positive integer, got %q", v)
			}
			a.tracks = n
		case "--ladders":
			v, err := need()
			if err != nil {
				return a, err
			}
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return a, fmt.Errorf("--ladders must be a positive integer, got %q", v)
			}
			a.ladders = n
		case "--segment":
			v, err := need()
			if err != nil {
				return a, err
			}
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return a, fmt.Errorf("--segment must be a positive Go duration, got %q", v)
			}
			a.segment = d
		case "--faults":
			v, err := need()
			if err != nil {
				return a, err
			}
			if v != "all" && v != "none" {
				return a, fmt.Errorf("--faults must be all|none, got %q", v)
			}
			a.faults = v
		case "--resume":
			a.resume = true
		default:
			return a, fmt.Errorf("unknown flag %q", arg)
		}
	}
	if a.work == "" {
		return a, fmt.Errorf("--work is required")
	}
	return a, nil
}

func runDemo(argv []string) int {
	args, err := parseDemoArgs(argv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argument error: %v\n", err)
		usage()
		return exitUsage
	}
	if err := os.MkdirAll(args.work, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create work dir: %v\n", err)
		return exitDataFault
	}
	// Fault injection runs against a dedicated copy so the deterministic
	// happy-path outputs remain reproducible across reruns.
	st := newDemoState()
	faultCode := injectAndProbeFaults(args, st)
	if faultCode != exitOK {
		return faultCode
	}
	report, code := assembleAndExecute(args, st)
	if code != exitOK {
		return code
	}
	printReport(args, report, st)
	return exitOK
}
