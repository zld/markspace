// Command mediad is the acceptance entry point for the bounded-memory
// MPEG-TS inspection and re-packaging kernel.
//
// Usage:
//
//	mediad demo   [--corrupt MODE] [--interrupt-after segment:N] --out DIR
//	mediad resume --out DIR
//	mediad verify --out DIR
//
// Exit codes: 0 success, 1 media/dependency failure, 2 usage error.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"mediad/internal/engine"
	"mediad/internal/fixture"
	"mediad/internal/model"
	"mediad/internal/verify"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return model.ExitUsage
	}
	cmd := args[0]
	flags, err := parseFlags(args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediad: %v\n", err)
		usage()
		return model.ExitUsage
	}
	out, ok := flags["--out"]
	if !ok || out == "" {
		fmt.Fprintln(os.Stderr, "mediad: --out DIR is required")
		usage()
		return model.ExitUsage
	}

	switch cmd {
	case "demo":
		return runDemo(flags, out)
	case "resume":
		return runResume(out)
	case "verify":
		return runVerify(out)
	case "-h", "--help", "help":
		usage()
		return model.ExitOK
	default:
		fmt.Fprintf(os.Stderr, "mediad: unknown command %q\n", cmd)
		usage()
		return model.ExitUsage
	}
}

func runDemo(flags map[string]string, out string) int {
	cfg := engine.Config{OutDir: out}
	if mode, ok := flags["--corrupt"]; ok {
		switch mode {
		case fixture.CorruptNone, fixture.CorruptSync, fixture.CorruptTruncated,
			fixture.CorruptContinuity, fixture.CorruptPCR, fixture.CorruptPSI:
			cfg.CorruptMode = mode
		default:
			fmt.Fprintf(os.Stderr, "mediad: unknown --corrupt mode %q\n", mode)
			return model.ExitUsage
		}
	}
	if spec, ok := flags["--interrupt-after"]; ok {
		if !validInterrupt(spec) {
			fmt.Fprintf(os.Stderr, "mediad: invalid --interrupt-after %q\n", spec)
			return model.ExitUsage
		}
		cfg.InterruptAt = spec
	}

	rep, err := engine.Run(context.Background(), cfg)
	if errors.Is(err, engine.ErrInterrupted) {
		// Interruption is an expected state: print what was committed and
		// exit zero so the harness can run resume next.
		printReport(rep)
		return model.ExitOK
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediad: run failed: %v\n", err)
		return model.ExitMediaFail
	}
	printReport(rep)
	if rep.CorruptionOffset != 0 {
		// Corruption was located and reported in-band: the acceptance
		// contract requires a non-zero exit for the corrupt path.
		return model.ExitMediaFail
	}
	return model.ExitOK
}

func runResume(out string) int {
	rep, err := engine.Run(context.Background(), engine.Config{OutDir: out})
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediad: resume failed: %v\n", err)
		return model.ExitMediaFail
	}
	printReport(rep)
	return model.ExitOK
}

func runVerify(out string) int {
	res, err := verify.Run(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediad: verify failed: %v\n", err)
		return model.ExitMediaFail
	}
	raw, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(raw))
	return model.ExitOK
}

// parseFlags accepts --key value pairs (and --key=value forms).
func parseFlags(args []string) (map[string]string, error) {
	flags := map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		key, val, hasVal := a, "", false
		if eq := indexByte(a, '='); eq >= 0 {
			key, val, hasVal = a[:eq], a[eq+1:], true
		}
		if len(key) < 2 || key[0] != '-' || key[1] != '-' {
			return nil, fmt.Errorf("unexpected argument %q", a)
		}
		if !hasVal {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag %s needs a value", key)
			}
			val = args[i+1]
			i++
		}
		flags[key] = val
	}
	return flags, nil
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func validInterrupt(spec string) bool {
	if len(spec) < 9 || spec[:8] != "segment:" {
		return false
	}
	n := 0
	for _, c := range spec[8:] {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
	}
	return spec[8:] != "" && n >= 0
}

func printReport(r *engine.Report) {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediad: cannot encode report: %v\n", err)
		return
	}
	fmt.Println(string(raw))
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  mediad demo   --out DIR [--corrupt sync|truncated|continuity|pcr|psi]
                [--interrupt-after segment:N]
  mediad resume --out DIR
  mediad verify --out DIR`)
}
