// Command mediacheck is the terminal entry point for the media
// processing kernel. The demo subcommand generates all synthetic
// scenarios, renders the per-sample and stable-summary reports and
// writes an MP4 plus manifests into an output directory.
//
// Exit codes follow the acceptance contract:
//
//	0 - success
//	1 - bad arguments or internal error
//	2 - input damaged but the report is complete
//	3 - migration failed and was rolled back
//
// Running every scenario (the default) is a self-check that exits 0 when
// every injected behavior produced its expected result; use --scenario to
// run one scenario and observe its specific exit code.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"mediacheck/internal/demo"
	"mediacheck/internal/fixture"
	"mediacheck/internal/manifest"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(demo.ExitUsage)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
		os.Exit(demo.ExitOK)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		usage()
		os.Exit(demo.ExitUsage)
	}
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	seed := fs.Int64("seed", 7, "deterministic RNG seed")
	out := fs.String("out", "", "output directory for reports and artifacts")
	scenario := fs.String("scenario", "all", "single scenario to run (default: all)")
	if err := fs.Parse(args); err != nil {
		return demo.ExitUsage
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "demo requires --out")
		return demo.ExitUsage
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create output dir: %v\n", err)
		return demo.ExitUsage
	}

	runner := demo.NewRunner(*seed)
	all, err := runner.RunAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "scenario generation failed: %v\n", err)
		return demo.ExitUsage
	}
	selected, err := selectScenarios(all, *scenario)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return demo.ExitUsage
	}

	report := &bytes.Buffer{}
	demo.Render(report, selected)
	summary := &bytes.Buffer{}
	demo.StableSummary(summary, selected)

	if err := writeFile(filepath.Join(*out, "report.txt"), report.Bytes()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return demo.ExitUsage
	}
	if err := writeFile(filepath.Join(*out, "summary.txt"), summary.Bytes()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return demo.ExitUsage
	}
	writeManifests(*out, selected)
	writeSampleMP4(*out, selected)

	// Echo the report to stdout as required by the terminal contract.
	os.Stdout.Write(report.Bytes())
	os.Stdout.Write(summary.Bytes())

	if *scenario == "all" {
		return demo.ExitOK
	}
	return demo.ExitCode(selected)
}

func selectScenarios(all []demo.ScenarioResult, name string) ([]demo.ScenarioResult, error) {
	if name == "all" {
		return all, nil
	}
	valid := map[string]bool{
		fixture.ScenarioNormal: true, fixture.ScenarioReorderLoss: true,
		fixture.ScenarioTruncatedNAL: true, fixture.ScenarioReplace: true,
		fixture.ScenarioMetadata: true, fixture.ScenarioCommitAbort: true,
	}
	if !valid[name] {
		return nil, fmt.Errorf("unknown scenario %q", name)
	}
	for _, r := range all {
		if r.Name == name {
			return []demo.ScenarioResult{r}, nil
		}
	}
	return nil, fmt.Errorf("scenario %q not produced", name)
}

func writeManifests(dir string, results []demo.ScenarioResult) {
	adapter := manifest.NewAdapter()
	for _, r := range results {
		var buf bytes.Buffer
		if err := adapter.Write(&buf, r.Manifest); err != nil {
			fmt.Fprintf(os.Stderr, "manifest write failed for %s: %v\n", r.Name, err)
			continue
		}
		name := filepath.Join(dir, "manifest-"+r.Name+".json")
		if err := writeFile(name, buf.Bytes()); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}
}

func writeSampleMP4(dir string, results []demo.ScenarioResult) {
	// Use the first scenario's clean/damaged report to exercise MP4 IO.
	if len(results) == 0 {
		return
	}
	buf := newSeekableBuffer()
	if err := demo.WriteMP4(buf, results[0].Report); err != nil {
		fmt.Fprintf(os.Stderr, "mp4 write failed: %v\n", err)
		return
	}
	if err := writeFile(filepath.Join(dir, "sample.mp4"), buf.Bytes()); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mediacheck demo --seed <int> --out <dir> [--scenario <name>]")
	fmt.Fprintln(os.Stderr, "scenarios: normal, reorder_loss, truncated_nal,",
		"same_path_replace, metadata_update, commit_interrupt, all")
}

// seekableBuffer is a small in-memory io.WriteSeeker for the MP4 writer.
type seekableBuffer struct {
	data []byte
	pos  int64
}

func newSeekableBuffer() *seekableBuffer { return &seekableBuffer{} }

func (b *seekableBuffer) Write(p []byte) (int, error) {
	end := b.pos + int64(len(p))
	if int(end) > len(b.data) {
		grown := make([]byte, end)
		copy(grown, b.data)
		b.data = grown
	}
	copy(b.data[b.pos:end], p)
	b.pos = end
	return len(p), nil
}

func (b *seekableBuffer) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case 0:
		next = offset
	case 1:
		next = b.pos + offset
	case 2:
		next = int64(len(b.data)) + offset
	}
	if next < 0 {
		return 0, fmt.Errorf("negative seek")
	}
	if int(next) > len(b.data) {
		grown := make([]byte, next)
		copy(grown, b.data)
		b.data = grown
	}
	b.pos = next
	return next, nil
}

func (b *seekableBuffer) Bytes() []byte { return b.data }
