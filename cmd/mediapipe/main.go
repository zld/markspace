// Command mediapipe is the acceptance-test adapter. It exposes a terminal-only
// demo that generates synthetic material, runs the full pipeline and prints
// the statistics the specification requires.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"mediapipe/internal/pipeline"
	"mediapipe/internal/testdata"
)

const (
	exitOK        = 0
	exitInvariant = 1
	exitRecovered = 2
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitInvariant)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	case "version":
		fmt.Println("mediapipe demo 1.0.0")
		os.Exit(exitOK)
	case "-h", "--help", "help":
		usage()
		os.Exit(exitOK)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		usage()
		os.Exit(exitInvariant)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `mediapipe - local media pipeline demo

usage:
  mediapipe demo [flags]

flags:
  --seed int          deterministic RNG seed (default 42)
  --target-kbps int   bandwidth policy target in kbps (default 900)
  --drift-ppm float   injected RTP clock drift in PPM (default 0)
  --wrap-rtp          force a 32-bit RTP timestamp wrap
  --vfr               emit variable frame-rate timing with dup-PTS anomaly
  --corrupt string    corruption class: truncated-box|truncated-klv|
                      conflict-metadata|rtp-wrap|clock-drift|vfr-dup-pts
  --recover           mark recoverable units as resynchronized
  --workdir string    output directory (default ./.work/demo)
  --write-manifest    also write the sealed JSON manifest under workdir
`)
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	seed := fs.Int64("seed", 42, "deterministic RNG seed")
	targetKbps := fs.Int("target-kbps", 900, "target bitrate in kbps")
	driftPPM := fs.Float64("drift-ppm", 0, "injected clock drift in PPM")
	wrap := fs.Bool("wrap-rtp", false, "force RTP timestamp wrap")
	vfr := fs.Bool("vfr", false, "variable frame rate timing")
	corrupt := fs.String("corrupt", "", "corruption class")
	recover := fs.Bool("recover", false, "recover damaged units")
	workdir := fs.String("workdir", ".work/demo", "output directory")
	writeManifest := fs.Bool("write-manifest", false, "write sealed manifest")
	if err := fs.Parse(args); err != nil {
		return exitInvariant
	}

	corruption, ok := resolveCorruption(*corrupt)
	if !ok {
		fmt.Fprintf(os.Stderr, "invalid --corrupt value: %q\n", *corrupt)
		return exitInvariant
	}
	// Named anomaly classes imply their pipeline toggles.
	if corruption == testdata.CorruptRTPWrap {
		*wrap = true
	}
	if corruption == testdata.CorruptVFRDupPTS {
		*vfr = true
	}
	if corruption == testdata.CorruptDrift && *driftPPM == 0 {
		*driftPPM = 180
	}
	if err := os.MkdirAll(filepath.Clean(*workdir), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create workdir: %v\n", err)
		return exitInvariant
	}

	opts := pipeline.Options{
		WorkDir: filepath.Clean(*workdir), Seed: *seed,
		TargetKbps: *targetKbps, DriftPPM: *driftPPM, WrapRTP: *wrap,
		VFR: *vfr, Corruption: corruption, Recover: *recover, ClockRate: 48000,
	}
	res, runErr := pipeline.Run(opts)
	if res == nil {
		fmt.Fprintf(os.Stderr, "pipeline failed before producing output: %v\n", runErr)
		return exitInvariant
	}
	if *recover {
		res.Stats.RecoveredUnits = pipeline.MarkRecovered(res.Errors)
	}
	if *writeManifest && len(res.ManifestBytes) > 0 {
		path := filepath.Join(opts.WorkDir, "manifest.json")
		if err := os.WriteFile(path, res.ManifestBytes, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "cannot write manifest: %v\n", err)
			return exitInvariant
		}
	}

	printReport(res, corruption)

	// Exit-code policy:
	//   1 = parameter/build/invariant failure (a non-recoverable fault).
	//   2 = damaged input that was recovered around while still emitting a
	//       manifest (a container-level corruption fault).
	//   0 = clean completion; expected network/clock anomalies do not fail it.
	if pipeline.HasFatal(res.Errors) {
		return exitInvariant
	}
	if res.Errors.HasContainerDamage() {
		return exitRecovered
	}
	return exitOK
}

func resolveCorruption(name string) (testdata.Corruption, bool) {
	if name == "" {
		return "", true
	}
	for _, c := range testdata.AllCorruptions() {
		if string(c) == name {
			return c, true
		}
	}
	return "", false
}

func printReport(res *pipeline.Result, corruption testdata.Corruption) {
	s := res.Stats
	report := map[string]any{
		"corruption":             string(corruption),
		"target_kbps":            s.TargetKbps,
		"actual_kbps":            round2(s.ActualKbps),
		"bitrate_deviation_pct":  round2(s.BitrateDeviationPct),
		"audio_rms":              round2(s.AudioRMS),
		"silence_ms":             s.SilenceMs,
		"video_hashes":           collectVideoHashes(res),
		"manifest_alignment_ms":  round2(s.ManifestAlignmentMs),
		"recovered_units":        s.RecoveredUnits,
		"source_error_offset":    normalizeOffset(s.SourceErrorOffset),
		"quarantined_packets":    s.QuarantinedPackets,
		"wrap_count":             s.WrapCount,
		"duplicate_matches":      s.DuplicateMatches,
		"segments":               s.Segments,
		"manifest_content_hash":  res.Manifest.ContentHash,
		"fault_count":            res.Errors.Len(),
	}
	body, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(body))
	if res.Errors.Len() > 0 {
		fmt.Fprintln(os.Stderr, "faults:")
		for _, e := range res.Errors.All() {
			fmt.Fprintf(os.Stderr, "  - %s\n", e.Error())
		}
	}
}

func collectVideoHashes(res *pipeline.Result) []string {
	seen := map[string]bool{}
	var out []string
	for _, seg := range res.Manifest.Segments {
		if seg.VideoHash != "" && !seen[seg.VideoHash] {
			seen[seg.VideoHash] = true
			out = append(out, seg.VideoHash)
		}
	}
	return out
}

func normalizeOffset(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func round2(v float64) float64 {
	return float64(int64(v*100+sign(v)*0.5)) / 100
}

func sign(v float64) float64 {
	if v < 0 {
		return -0.5
	}
	return 0.5
}
