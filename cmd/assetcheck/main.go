// Command assetcheck is the terminal acceptance entry point for the offline
// media processing kernel. It emits machine-readable JSON and uses the
// documented exit codes: 0 spec passed, 1 business validation failure,
// 2 argument/runtime error.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	mk "github.com/local/mediakernel"
)

// DefaultWorkDir is the fixed state directory shared between demo and verify
// invocations so manifests and incremental state survive across commands.
const DefaultWorkDir = ".assetcheck-state"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage()
		return mk.ExitRuntimeError
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "-h", "--help", "help":
		printUsage()
		return mk.ExitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		printUsage()
		return mk.ExitRuntimeError
	}
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	seed := fs.Int64("seed", 7, "deterministic fixture seed")
	queueCap := fs.Int("queue-cap", 8, "bounded pipeline queue capacity")
	slowMs := fs.Int("slow-consumer-ms", 0, "artificial per-packet consumer delay in milliseconds")
	manifestVersion := fs.Int("manifest-version", mk.ManifestCurrent, "manifest schema version to leave on disk (1 or 2)")
	corrupt := fs.String("corrupt", "", "corruption mode: truncated | invalid-duration | bad-sequence")
	expectError := fs.Bool("expect-error", false, "expect a non-zero exit; fail if the run succeeds")
	interrupt := fs.String("interrupt-migration", "", "interrupt the migration: after-validate | after-stage | after-commit")
	workDir := fs.String("work-dir", DefaultWorkDir, "state directory for manifests and incremental index")
	if err := fs.Parse(args); err != nil {
		return mk.ExitRuntimeError
	}

	opts := mk.DemoOptions{
		Seed:            *seed,
		QueueCap:        *queueCap,
		SlowConsumerMs:  *slowMs,
		ManifestVersion: *manifestVersion,
		Corrupt:         mk.CorruptMode(*corrupt),
		ExpectError:     *expectError,
		InterruptAfter:  mapInterrupt(*interrupt),
		WorkDir:         *workDir,
	}
	if err := validateDemoOpts(opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return mk.ExitRuntimeError
	}

	result, err := mk.RunDemo(context.Background(), opts)
	if result == nil {
		emitJSON(map[string]any{"fatal": err.Error()})
		return mk.ClassifyExitCode(err)
	}
	emitJSON(result)

	if err != nil {
		if *expectError {
			return mk.ClassifyExitCode(err) // expected failure still exits non-zero
		}
		return mk.ClassifyExitCode(err)
	}
	if *expectError {
		fmt.Fprintln(os.Stderr, "--expect-error set but the run succeeded")
		return mk.ExitRuntimeError
	}
	if !result.SpecPass {
		return mk.ExitBusinessFail
	}
	return mk.ExitOK
}

func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	repeat := fs.Int("repeat", 2, "number of repeated verification runs")
	printJSON := fs.Bool("print-json", false, "emit the verification document as JSON")
	workDir := fs.String("work-dir", DefaultWorkDir, "state directory to verify")
	if err := fs.Parse(args); err != nil {
		return mk.ExitRuntimeError
	}
	result, err := mk.Verify(context.Background(), *workDir, *repeat)
	if err != nil {
		if result != nil && *printJSON {
			emitJSON(result)
		} else {
			emitJSON(map[string]any{"fatal": err.Error()})
		}
		return mk.ClassifyExitCode(err)
	}
	if *printJSON {
		emitJSON(result)
	} else {
		emitJSON(result)
	}
	if !result.Stable || !result.SpecPass {
		return mk.ExitBusinessFail
	}
	return mk.ExitOK
}

func mapInterrupt(token string) string {
	switch token {
	case "":
		return ""
	case "after-validate":
		return mk.StageValidate
	case "after-stage":
		return mk.StageStage
	case "after-commit":
		return mk.StageCommit
	default:
		return token
	}
}

func validateDemoOpts(o mk.DemoOptions) error {
	switch o.Corrupt {
	case mk.CorruptNone, mk.CorruptTruncated, mk.CorruptInvalidDuration, mk.CorruptSequence:
	default:
		return fmt.Errorf("invalid --corrupt mode %q", o.Corrupt)
	}
	if o.QueueCap < 1 {
		return fmt.Errorf("--queue-cap must be >= 1, got %d", o.QueueCap)
	}
	if o.ManifestVersion != mk.ManifestV1 && o.ManifestVersion != mk.ManifestCurrent {
		return fmt.Errorf("--manifest-version must be %d or %d", mk.ManifestV1, mk.ManifestCurrent)
	}
	return nil
}

func emitJSON(v any) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to encode JSON: %v\n", err)
		return
	}
	os.Stdout.Write(raw)
	os.Stdout.Write([]byte("\n"))
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `assetcheck - offline media kernel acceptance CLI

usage:
  assetcheck demo [flags]
    --seed int                 deterministic fixture seed (default 7)
    --queue-cap int            bounded queue capacity (default 8)
    --slow-consumer-ms int     artificial consumer delay in ms
    --manifest-version int     manifest schema left on disk: 1 or 2
    --corrupt string           truncated | invalid-duration | bad-sequence
    --expect-error             require a non-zero exit
    --interrupt-migration s    after-validate | after-stage | after-commit
    --work-dir string          state directory (default ".assetcheck-state")
  assetcheck verify [flags]
    --repeat int               repeated verification runs (default 2)
    --print-json               print the JSON verification document
    --work-dir string          state directory (default ".assetcheck-state")

exit codes: 0 spec passed | 1 business validation failure | 2 argument/runtime error`)
}
