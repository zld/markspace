// Command mediad is the terminal entry point for the media processing kernel.
//
// It repairs timestamps, rebuilds a random-access index, fans repaired media
// out to three bitrate representations, recovers each representation's RTP
// stream independently, merges them back onto the shared clock and prints a
// JSON summary. Input is always generated in memory, so no network or
// external fixture is needed.
//
// Exit codes: 0 success, 2 damaged input but a recovery summary was produced,
// 1 execution failure.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"mediad/internal/fanout"
	"mediad/internal/merr"
	"mediad/internal/pipeline"
	"mediad/internal/testkit"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage())
		return 1
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "corrupt":
		return runCorrupt(args[1:], stdout, stderr)
	case "verify":
		return runVerify(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, usage())
		return 0
	default:
		fmt.Fprintf(stderr, "mediad: unknown command %q\n\n%s\n", args[0], usage())
		return 1
	}
}

func usage() string {
	return `mediad - local Matroska media repair and RTP aggregation

Usage:
  mediad demo    [--seed N] [--slow-consumer-ms N] [--loss-every N] [--fault NAME|--corrupt NAME]
  mediad corrupt [--seed N] [--fault NAME|--corrupt NAME]
  mediad verify  [--seed N] [--repeat N]

Faults: ` + faultList() + `

Exit codes: 0 success, 2 damaged input with a recovery summary, 1 failure.`
}

func faultList() string {
	out := ""
	for i, f := range testkit.KnownFaults {
		if i > 0 {
			out += ", "
		}
		out += f
	}
	return out
}

// commonFlags are shared by every subcommand.
type commonFlags struct {
	seed int64
}

func addCommon(fs *flag.FlagSet, c *commonFlags) {
	fs.Int64Var(&c.seed, "seed", 42, "deterministic fixture seed")
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c commonFlags
	addCommon(fs, &c)
	slowMs := fs.Int("slow-consumer-ms", 120, "consumer delay per batch, in milliseconds")
	lossEvery := fs.Int("loss-every", 17, "drop every N-th packet (0 disables)")
	fault := fs.String("fault", testkit.FaultNone, "input fault to inject")
	corrupt := fs.String("corrupt", "", "alias for --fault, accepted for the documented demo invocation")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *slowMs < 0 || *lossEvery < 0 {
		fmt.Fprintln(stderr, "mediad demo: --slow-consumer-ms and --loss-every must be >= 0")
		return 1
	}
	if *corrupt != "" {
		if *fault != testkit.FaultNone && *fault != *corrupt {
			fmt.Fprintln(stderr, "mediad demo: --fault and --corrupt disagree")
			return 1
		}
		*fault = *corrupt
	}
	switch *fault {
	case testkit.FaultNone, testkit.FaultRTPLoss, testkit.FaultSlowConsumer,
		testkit.FaultClusterTruncate, testkit.FaultTimestampWrap, testkit.FaultUnknownSizeNest:
	default:
		fmt.Fprintf(stderr, "mediad demo: unknown fault %q\n", *fault)
		return 1
	}

	fixture, err := testkit.BuildFaulted(*fault, c.seed)
	if err != nil {
		fmt.Fprintf(stderr, "mediad demo: %v\n", err)
		return 1
	}
	opts := pipeline.Options{SlowConsumer: time.Duration(*slowMs) * time.Millisecond}
	if *lossEvery > 0 {
		opts.LayerTransport = map[string]fanout.Transport{
			fanout.LadderNames[0]: {DropEvery: *lossEvery},
			fanout.LadderNames[1]: {DropEvery: *lossEvery, SwapEvery: *lossEvery + 6},
			fanout.LadderNames[2]: {DropEvery: *lossEvery},
		}
	}
	return execute(fixture.Data, opts, false, stdout, stderr)
}

func runCorrupt(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("corrupt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c commonFlags
	addCommon(fs, &c)
	fault := fs.String("fault", testkit.FaultClusterTruncate, "input fault to inject")
	corrupt := fs.String("corrupt", "", "alias for --fault")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *corrupt != "" && *fault != testkit.FaultClusterTruncate && *fault != *corrupt {
		fmt.Fprintln(stderr, "mediad corrupt: --fault and --corrupt disagree")
		return 1
	}
	if *corrupt != "" {
		*fault = *corrupt
	}
	if *fault == testkit.FaultNone {
		fmt.Fprintln(stderr, "mediad corrupt: --fault must name a corruption")
		return 1
	}

	fixture, err := testkit.BuildFaulted(*fault, c.seed)
	if err != nil {
		fmt.Fprintf(stderr, "mediad corrupt: %v\n", err)
		return 1
	}
	opts := pipeline.Options{}
	if fixture.LossEvery > 0 {
		opts.LayerTransport = map[string]fanout.Transport{
			fanout.LadderNames[0]: {DropEvery: fixture.LossEvery},
			fanout.LadderNames[1]: {DropEvery: fixture.LossEvery},
			fanout.LadderNames[2]: {DropEvery: fixture.LossEvery},
		}
	}
	if fixture.SlowConsumerMs > 0 {
		opts.SlowConsumer = time.Duration(fixture.SlowConsumerMs) * time.Millisecond
	}
	opts.CancelLayer = fixture.CancelLayer
	return execute(fixture.Data, opts, true, stdout, stderr)
}

// execute runs the pipeline and prints its summary.
func execute(data []byte, opts pipeline.Options, expectDamage bool, stdout, stderr io.Writer) int {
	ctx := context.Background()
	out, err := pipeline.Run(ctx, data, opts)
	if err != nil {
		// Damage that reached the reporting stage is printed in the summary;
		// here there is no summary, so only the failure itself remains.
		fmt.Fprintf(stderr, "mediad: %v\n", err)
		return merr.ExitCode(err)
	}
	if err := printSummary(stdout, out.Summary); err != nil {
		fmt.Fprintf(stderr, "mediad: %v\n", err)
		return 1
	}
	switch {
	case out.Damage != nil:
		fmt.Fprintf(stderr, "mediad: recovered from damaged input: %v\n", out.Damage)
		return 2
	case expectDamage:
		// The caller asked for a corruption that turned out to be
		// non-fatal; report success rather than inventing an exit code.
		return 0
	}
	return 0
}

func printSummary(w io.Writer, s interface{}) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

func runVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c commonFlags
	addCommon(fs, &c)
	repeat := fs.Int("repeat", 2, "number of full runs to compare")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *repeat < 2 {
		fmt.Fprintln(stderr, "mediad verify: --repeat must be at least 2")
		return 1
	}

	hashes := make([]string, 0, *repeat)
	digests := make([]string, 0, *repeat)
	var first *pipeline.Output
	for i := 0; i < *repeat; i++ {
		fixture, err := testkit.BuildFaulted(testkit.FaultNone, c.seed)
		if err != nil {
			fmt.Fprintf(stderr, "mediad verify: %v\n", err)
			return 1
		}
		out, err := pipeline.Run(context.Background(), fixture.Data, pipeline.Options{})
		if err != nil {
			fmt.Fprintf(stderr, "mediad verify: run %d: %v\n", i+1, err)
			return 1
		}
		if i == 0 {
			first = out
		}
		hashes = append(hashes, out.Summary.SummaryHash)
		digests = append(digests, out.Index.Index.Digest)
	}

	equal := true
	for i := 1; i < len(hashes); i++ {
		if hashes[i] != hashes[0] {
			equal = false
		}
	}
	indexEqual := true
	for i := 1; i < len(digests); i++ {
		if digests[i] != digests[0] {
			indexEqual = false
		}
	}

	report := struct {
		SummaryHashEqual bool     `json:"summary_hash_equal"`
		IndexDigestEqual bool     `json:"index_digest_equal"`
		Runs             int      `json:"runs"`
		SummaryHash      string   `json:"summary_hash"`
		IndexDigest      string   `json:"index_digest"`
		AllHashes        []string `json:"summary_hashes"`
		IndexEntries     int      `json:"index_entries"`
	}{
		SummaryHashEqual: equal,
		IndexDigestEqual: indexEqual,
		Runs:             *repeat,
		SummaryHash:      hashes[0],
		IndexDigest:      digests[0],
		AllHashes:        hashes,
		IndexEntries:     len(first.Index.Index.Entries),
	}
	if err := printSummary(stdout, report); err != nil {
		fmt.Fprintf(stderr, "mediad verify: %v\n", err)
		return 1
	}
	if !equal || !indexEqual {
		fmt.Fprintln(stderr, "mediad verify: repeated runs disagree")
		return 1
	}
	return 0
}
