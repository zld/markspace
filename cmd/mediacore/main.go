// Command mediacore is the terminal entry point for the local media merge
// kernel. The core library owns all processing; this package only parses
// flags, drives a run and presents the result.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mediacore"
)

// Exit codes follow the acceptance contract.
const (
	exitOK       = 0
	exitInternal = 1
	exitReject   = 2
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitInternal)
	}
	code := run(os.Args[1], os.Args[2:])
	os.Exit(code)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mediacore <demo|validate|run> [flags]")
	fmt.Fprintln(os.Stderr, "  demo   --seed N --budget-ms N [--recover]")
	fmt.Fprintln(os.Stderr, "  validate <manifest-file>")
	fmt.Fprintln(os.Stderr, "  run    --manifest <file> --segments <dir> --budget-ms N")
}

func run(cmd string, args []string) int {
	switch cmd {
	case "demo":
		return runDemo(args)
	case "validate":
		return runValidate(args)
	case "run":
		return runPipeline(args)
	default:
		usage()
		return exitInternal
	}
}

type flags struct {
	seed       int64
	budgetMS   int64
	recover    bool
	manifest   string
	segments   string
	positional []string
}

func parseFlags(args []string) (*flags, error) {
	f := &flags{seed: 1, budgetMS: 120}
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag %s needs a value", a)
			}
			i++
			return args[i], nil
		}
		switch {
		case a == "--recover":
			f.recover = true
		case strings.HasPrefix(a, "--seed"):
			v, err := flagValue(a, next)
			if err != nil {
				return nil, err
			}
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("bad --seed: %w", err)
			}
			f.seed = n
		case strings.HasPrefix(a, "--budget-ms"):
			v, err := flagValue(a, next)
			if err != nil {
				return nil, err
			}
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("bad --budget-ms: %w", err)
			}
			f.budgetMS = n
		case strings.HasPrefix(a, "--manifest"):
			v, err := flagValue(a, next)
			if err != nil {
				return nil, err
			}
			f.manifest = v
		case strings.HasPrefix(a, "--segments"):
			v, err := flagValue(a, next)
			if err != nil {
				return nil, err
			}
			f.segments = v
		default:
			f.positional = append(f.positional, a)
		}
	}
	return f, nil
}

// flagValue supports both "--k=v" and "--k v" forms.
func flagValue(a string, next func() (string, error)) (string, error) {
	if i := strings.IndexByte(a, '='); i >= 0 {
		return a[i+1:], nil
	}
	return next()
}

func runDemo(args []string) int {
	f, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "input rejected:", err)
		return exitReject
	}
	sc := mediacore.BuildScenario(f.seed)
	dir, err := os.MkdirTemp("", "mediacore-demo-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "internal failure:", err)
		return exitInternal
	}
	journalPath := filepath.Join(dir, "journal.json")

	cfg := mediacore.RunConfig{
		BudgetMS:       f.budgetMS,
		MemBudgetBytes: 1 << 24,
		PerItemCostMS:  45, // deterministic charge so the budget verdict is reproducible
		JournalPath:    journalPath,
	}
	first, err := mediacore.Run(sc, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "internal failure:", err)
		return exitInternal
	}
	fmt.Println("== first run ==")
	first.Report.Render(os.Stdout)

	if f.recover {
		// Recovery: same inputs, same journal. Committed set and the summary
		// counters must be identical to the first run.
		second, err := mediacore.Run(sc, cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "internal failure during recovery:", err)
			return exitInternal
		}
		fmt.Println("== recovery run ==")
		second.Report.Render(os.Stdout)
		if !sameKeys(first.Journal.Committed(), second.Journal.Committed()) {
			fmt.Fprintln(os.Stderr, "internal failure: recovery changed committed set")
			return exitInternal
		}
		fmt.Println("recovery: committed stable across runs = true")
	}

	// Any rejected item is an input rejection per the contract.
	if first.Report.Rejected > 0 {
		return exitReject
	}
	return exitOK
}

func runValidate(args []string) int {
	f, err := parseFlags(args)
	if err != nil || len(f.positional) != 1 {
		fmt.Fprintln(os.Stderr, "usage: mediacore validate <manifest-file>")
		return exitReject
	}
	raw, err := os.ReadFile(f.positional[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "input rejected:", err)
		return exitReject
	}
	if _, err := mediacore.ParsePlaylist(filepath.Base(f.positional[0]),
		stringReader(raw)); err != nil {
		fmt.Fprintln(os.Stderr, "input rejected:", err)
		return exitReject
	}
	fmt.Println("manifest valid:", f.positional[0])
	return exitOK
}

func runPipeline(args []string) int {
	f, err := parseFlags(args)
	if err != nil || f.manifest == "" {
		fmt.Fprintln(os.Stderr, "usage: mediacore run --manifest <file> --segments <dir> --budget-ms N")
		return exitReject
	}
	// The run command demonstrates the exit-2 path on a manifest that fails
	// structural validation; full filesystem segment loading is exercised by
	// the generated in-memory demo instead.
	raw, err := os.ReadFile(f.manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "input rejected:", err)
		return exitReject
	}
	if _, err := mediacore.ParsePlaylist(filepath.Base(f.manifest), stringReader(raw)); err != nil {
		fmt.Fprintln(os.Stderr, "input rejected:", err)
		return exitReject
	}
	fmt.Println("manifest accepted; use demo for the generated end-to-end run")
	return exitOK
}

func sameKeys(a, b []mediacore.Key) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stringReader(b []byte) *strings.Reader { return strings.NewReader(string(b)) }
