// Command regress is the terminal entry point for the live multi-bitrate
// regression harness. It generates self-contained fixtures, reconstructs the
// processing state from media timestamps and event records, and prints
// reproducible delay/loss/duplicate/evidence numbers plus a manifest hash.
//
// Usage:
//
//	go run ./cmd/regress demo --work <dir> --fault <none|wrap|truncate|after-artifact|events>
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"regress/internal/fixture"
	"regress/internal/measure"
	"regress/internal/model"
	"regress/internal/pipeline"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		// Failures must never look like success: print the locatable stage
		// error and exit non-zero.
		fmt.Fprintf(os.Stderr, "regress: %v\n", err)
		if me := model.AsError(err); me != nil {
			fmt.Fprintf(os.Stderr, "stage=%s file=%s", me.Stage, me.File)
			if me.Track != "" {
				fmt.Fprintf(os.Stderr, " track=%s", me.Track)
			}
			if me.Offset > 0 {
				fmt.Fprintf(os.Stderr, " offset=%d", me.Offset)
			}
			fmt.Fprintln(os.Stderr)
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("missing subcommand; expected: demo")
	}
	if args[0] != "demo" {
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	work := fs.String("work", "", "confined working directory (required)")
	faultName := fs.String("fault", "none",
		"fault scenario: none|wrap|truncate|after-artifact|events")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *work == "" {
		return errors.New("--work is required")
	}
	abs, err := filepath.Abs(*work)
	if err != nil {
		return err
	}
	fault := fixture.Fault(*faultName)

	res, err := pipeline.Run(abs, fault)
	if err != nil {
		return err
	}
	printResult(res)
	return nil
}

func printResult(r *pipeline.Result) {
	c := r.Measure.Counts
	d := r.Measure.Delay
	fmt.Printf("fault-run complete\n")
	fmt.Printf("missing=%d duplicate=%d extra=%d conflict=%d\n",
		c.Missing, c.Duplicate, c.Extra, c.Conflict)
	fmt.Printf("delay_ticks: count=%d min=%d median=%d p90=%d p99=%d max=%d\n",
		d.Count, d.Min, d.Median, d.P90, d.P99, d.Max)
	fmt.Print(renderBoundaries(r.Measure))
	fmt.Print(r.Evidence.Summary())
	fmt.Printf("manifest_sha256=%s\n", r.ManifestSHA)
	fmt.Printf("recovered=%d republished=%d artifacts=%d\n",
		boolToInt(r.Recovered), r.Republished, r.ArtifactCount)
}

// renderBoundaries prints one line per rendition with its maximum grid
// boundary error in master ticks; acceptance requires this to stay <= 1.
func renderBoundaries(m *measure.Report) string {
	var out string
	for _, b := range m.Boundaries {
		out += fmt.Sprintf("boundary: rendition=%s samples=%d max_abs_error_tick=%d\n",
			b.Rendition, b.Samples, b.MaxAbsError)
	}
	return out
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
