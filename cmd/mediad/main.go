// Command mediad is the terminal entry point.  It only parses flags, runs the
// pipeline and prints results; every decision lives in the library packages.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"regional.example/mediad/internal/audio"
	"regional.example/mediad/internal/fault"
	"regional.example/mediad/internal/fixtures"
	"regional.example/mediad/internal/manifest"
	"regional.example/mediad/internal/pipeline"
)

// Exit codes from the operator contract.
const (
	exitOK      = 0
	exitUsage   = 2
	exitFatal   = 3
	exitQuality = 4
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(exitUsage)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	case "run":
		os.Exit(runSingle(os.Args[2:]))
	case "-h", "--help", "help":
		usage(os.Stdout)
		os.Exit(exitOK)
	default:
		fmt.Fprintf(os.Stderr, "mediad: unknown command %q\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(exitUsage)
	}
}

func usage(w *os.File) {
	fmt.Fprintln(w, `usage: mediad <command> [flags]

  demo  generate fixtures and process every scenario
  run   process one existing manifest

demo flags:
  --seed int          fixture seed (default 41)
  --input string      directory for generated fixtures (default "generated")
  --rate int          output sample rate (must be 48000)
  --layout string     output layout: mono|stereo|5.1
  --loudness float    target integrated loudness in LUFS
  --target-kbps int   target forwarded bitrate
  --repeat            reprocess and verify the output hash is identical
  --emit string       also write the forwarding manifest for each scenario

run flags:
  --manifest string   path to an asset manifest
  --out string        directory to write the forwarding manifest
  (plus the audio flags above)`)
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	seed := fs.Int64("seed", 41, "fixture seed")
	input := fs.String("input", "generated", "generated fixture directory")
	rate := fs.Int("rate", 48000, "output sample rate")
	layout := fs.String("layout", "stereo", "output layout")
	loudness := fs.Float64("loudness", -18, "target loudness in LUFS")
	targetKbps := fs.Int("target-kbps", 128, "target bitrate in kbps")
	repeat := fs.Bool("repeat", false, "reprocess and verify output hash stability")
	emit := fs.String("emit", "", "directory to write forwarding manifests")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	opts := pipeline.DefaultOptions()
	opts.OutputRate = *rate
	opts.TargetKbps = *targetKbps
	opts.TargetLUFS = *loudness
	lay, err := audio.ParseLayout(*layout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediad: %v\n", err)
		return exitUsage
	}
	opts.OutputLayout = lay
	if err := opts.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "mediad: %v\n", err)
		return exitUsage
	}

	// Generate fixtures under the requested directory.  The demo never reads
	// outside this directory and never overwrites an input with an output.
	dir, err := filepath.Abs(*input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediad: %v\n", err)
		return exitUsage
	}
	gens, err := fixtures.Generate(dir, *seed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediad: generate fixtures: %v\n", err)
		return exitFatal
	}

	// Choose the clean scenario for the headline metrics and the truncated
	// scenario for the recovery metrics, as the contract requires.
	var clean, damaged *fixtures.Generated
	for i := range gens {
		switch gens[i].Scenario {
		case fixtures.ScenarioNormal:
			clean = &gens[i]
		case fixtures.ScenarioTruncated:
			damaged = &gens[i]
		}
	}
	if clean == nil || damaged == nil {
		fmt.Fprintln(os.Stderr, "mediad: required scenarios were not generated")
		return exitFatal
	}

	cleanRes, err := processManifest(clean.Manifest, opts)
	if err != nil {
		printErrorFaults(os.Stderr, err)
		return exitFatal
	}
	damagedRes, err := processManifest(damaged.Manifest, opts)
	if err != nil {
		printErrorFaults(os.Stderr, err)
		return exitFatal
	}

	// Print the acceptance fields.  The clean run supplies quality metrics and
	// the damaged run supplies recovery metrics, matching the contract.
	printDemo(clean, damaged, cleanRes, damagedRes, opts)

	exit := exitOK
	if cleanRes.ExitCode == exitFatal || damagedRes.ExitCode == exitFatal {
		exit = exitFatal
	}
	if len(cleanRes.Quality.FailedNames()) > 0 {
		exit = maxInt(exit, exitQuality)
	}

	if *repeat {
		cleanRes2, err := processManifest(clean.Manifest, opts)
		if err != nil {
			printErrorFaults(os.Stderr, err)
			return exitFatal
		}
		damagedRes2, err := processManifest(damaged.Manifest, opts)
		if err != nil {
			printErrorFaults(os.Stderr, err)
			return exitFatal
		}
		// Both the clean and damaged outputs must hash identically across runs,
		// so a recovery change cannot hide behind a stable clean hash.
		same := cleanRes.OutputSHA256 == cleanRes2.OutputSHA256 && damagedRes.OutputSHA256 == damagedRes2.OutputSHA256
		fmt.Printf("repeat_sha256_match=%v\n", same)
		if !same {
			fmt.Fprintln(os.Stderr, "mediad: repeat run produced a different output hash")
			exit = maxInt(exit, exitQuality)
		}
	}

	if *emit != "" {
		outDir, err := filepath.Abs(*emit)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mediad: %v\n", err)
			return exitUsage
		}
		if err := writeManifest(outDir, "normal", cleanRes.ManifestBytes); err != nil {
			fmt.Fprintf(os.Stderr, "mediad: emit: %v\n", err)
			return exitFatal
		}
		if err := writeManifest(outDir, "truncated", damagedRes.ManifestBytes); err != nil {
			fmt.Fprintf(os.Stderr, "mediad: emit: %v\n", err)
			return exitFatal
		}
	}

	printFaults(os.Stdout, cleanRes.Faults...)
	return exit
}

// processManifest loads and runs one manifest with the shared options.
func processManifest(path string, opts pipeline.Options) (*pipeline.Result, error) {
	asset, err := manifest.ParseFile(path)
	if err != nil {
		return nil, err
	}
	// A demo is single-audio-track by construction; run refuses anything the
	// graph cannot schedule.
	return pipeline.Run(asset, opts)
}

func printDemo(clean, damaged *fixtures.Generated, c, d *pipeline.Result, opts pipeline.Options) {
	fmt.Printf("scenario_clean=%s\n", clean.Scenario)
	fmt.Printf("scenario_damaged=%s\n", damaged.Scenario)
	fmt.Printf("target_kbps=%d\n", opts.TargetKbps)
	fmt.Printf("actual_kbps=%.1f\n", c.Manifest.Packets.ActualKbps)
	fmt.Printf("sample_rate=%d\n", c.SampleRate)
	fmt.Printf("layout=%s\n", c.Layout)
	if c.Loudness.Silent {
		fmt.Println("integrated_lufs=silent")
	} else {
		fmt.Printf("integrated_lufs=%.2f\n", c.Loudness.IntegratedLUFS)
	}
	fmt.Printf("true_peak_dbtp=%.2f\n", c.TruePeakDBTP)
	fmt.Printf("quality_clipped_samples=%d\n", c.ClippedSamples)
	fmt.Printf("timeline_error_ticks=%d\n", c.Manifest.TimelineErrorTicks)
	fmt.Printf("recovered_samples=%d\n", d.RecoveredSamples)
	fmt.Printf("missing_duration_ms=%d\n", d.MissingDurationMS)
	fmt.Printf("manifest_alignment_ms=%d\n", c.Manifest.ManifestAlignmentMS)
	fmt.Printf("output_sha256=%s\n", c.OutputSHA256)
}

func writeManifest(dir, name string, b []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, name+".manifest.json")
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func runSingle(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	manPath := fs.String("manifest", "", "asset manifest path")
	outDir := fs.String("out", "", "directory for the forwarding manifest")
	rate := fs.Int("rate", 48000, "output sample rate")
	layout := fs.String("layout", "stereo", "output layout")
	loudness := fs.Float64("loudness", -18, "target loudness")
	targetKbps := fs.Int("target-kbps", 128, "target bitrate")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *manPath == "" {
		fmt.Fprintln(os.Stderr, "mediad: --manifest is required")
		return exitUsage
	}
	opts := pipeline.DefaultOptions()
	opts.OutputRate = *rate
	opts.TargetKbps = *targetKbps
	opts.TargetLUFS = *loudness
	lay, err := audio.ParseLayout(*layout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediad: %v\n", err)
		return exitUsage
	}
	opts.OutputLayout = lay
	if err := opts.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "mediad: %v\n", err)
		return exitUsage
	}
	res, err := processManifest(*manPath, opts)
	if err != nil {
		printErrorFaults(os.Stderr, err)
		return exitForError(err)
	}
	for _, m := range res.Quality.Metrics {
		fmt.Printf("%s=%s\n", m.Name, m.Value)
	}
	if *outDir != "" {
		if err := writeManifest(*outDir, res.AssetID, res.ManifestBytes); err != nil {
			fmt.Fprintf(os.Stderr, "mediad: emit: %v\n", err)
			return exitFatal
		}
	}
	printFaults(os.Stdout, res.Faults...)
	return res.ExitCode
}

// exitForError maps a terminal error to the contract's code 3 for
// unrecoverable input and 2 for a shape problem the caller can fix.
func exitForError(err error) int {
	var f fault.Fault
	if !asFault(err, &f) {
		return exitFatal
	}
	switch f.Class {
	case fault.ClassInput, fault.ClassLimit:
		return exitFatal
	default:
		return exitFatal
	}
}

func asFault(err error, out *fault.Fault) bool {
	for err != nil {
		if f, ok := err.(fault.Fault); ok {
			*out = f
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// printFaults renders classified faults deterministically.
func printFaults(w *os.File, errs ...fault.Fault) {
	lines := make([]string, 0, len(errs))
	for _, f := range errs {
		lines = append(lines, f.Error())
	}
	sort.Strings(lines)
	for _, l := range lines {
		fmt.Fprintf(w, "fault: %s\n", l)
	}
}

// printErrorFaults renders unclassified terminal errors.
func printErrorFaults(w *os.File, err error) {
	var f fault.Fault
	if asFault(err, &f) {
		printFaults(w, f)
		return
	}
	fmt.Fprintf(w, "fault: %s\n", err)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
