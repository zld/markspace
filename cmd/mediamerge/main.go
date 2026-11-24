// Command mediamerge is the terminal acceptance and demo entry point. It
// generates self-contained fixtures, runs the normal, damaged and recovered
// paths, writes a JSON manifest plus a local fingerprint index, and prints a
// JSON summary with the path exit codes.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mediamerge/internal/manifest"
	"mediamerge/internal/pipeline"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "demo" {
		fmt.Fprintln(os.Stderr, "usage: mediamerge demo [flags]")
		os.Exit(1)
	}
	os.Exit(runDemo(os.Args[2:], os.Stdout, os.Stderr))
}

type pathSummary struct {
	Name     string `json:"name"`
	ExitCode int    `json:"exit_code"`
}

type summary struct {
	Segments            int           `json:"segments"`
	SilenceMs           int64         `json:"silence_ms"`
	Duplicates          int           `json:"duplicates"`
	Collisions          int           `json:"collisions"`
	TargetBitrateBps    int64         `json:"target_bitrate_bps"`
	ActualBitrateBps    int64         `json:"actual_bitrate_bps"`
	BitrateDeviationPct float64       `json:"bitrate_deviation_pct"`
	QualitySamples      int64         `json:"quality_samples"`
	AlignmentErrorMs    float64       `json:"manifest_alignment_error_ms"`
	RecoveredTracks     []uint32      `json:"recovered_tracks"`
	IsolatedTracks      []uint32      `json:"isolated_tracks"`
	ManifestSHA256      string        `json:"manifest_sha256"`
	Paths               []pathSummary `json:"paths"`
	ManifestFile        string        `json:"manifest_file"`
	IndexFile           string        `json:"fingerprint_index_file"`
	Note                string        `json:"note"`
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	seed := fs.Int64("seed", 17, "fixture generation seed")
	target := fs.Int64("target-bitrate", 640000, "target bitrate in bits per second")
	segMs := fs.Int("segment-ms", 2000, "maximum segment duration in milliseconds")
	fixtureDir := fs.String("fixtures", "generated", "directory for generated fixtures")
	manifestPath := fs.String("manifest", "generated/manifest.json", "manifest output path")
	indexPath := fs.String("index", "", "fingerprint index path (default next to manifest)")
	if err := fs.Parse(args); err != nil {
		return pipeline.ExitBadInput
	}
	if *target <= 0 || *segMs <= 0 || *seed < 0 {
		fmt.Fprintln(stderr, "demo: --target-bitrate, --segment-ms must be positive and --seed non-negative")
		return pipeline.ExitBadInput
	}
	if *indexPath == "" {
		*indexPath = filepath.Join(filepath.Dir(*manifestPath), "fingerprints.json")
	}

	if err := os.MkdirAll(*fixtureDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "demo: fixture dir: %v\n", err)
		return pipeline.ExitBadInput
	}
	if err := os.MkdirAll(filepath.Dir(*manifestPath), 0o755); err != nil {
		fmt.Fprintf(stderr, "demo: manifest dir: %v\n", err)
		return pipeline.ExitBadInput
	}

	fixtures, err := pipeline.Generate(*fixtureDir, *seed, *segMs)
	if err != nil {
		fmt.Fprintf(stderr, "demo: %v\n", err)
		return pipeline.ExitBadInput
	}
	suite := pipeline.Run(fixtures, pipeline.Options{
		Seed: *seed, SegmentMs: *segMs, TargetBps: *target, FixtureDir: *fixtureDir,
	})

	hash, err := suite.Doc.Write(*manifestPath)
	if err != nil {
		fmt.Fprintf(stderr, "demo: %v\n", err)
		return pipeline.ExitBadInput
	}
	if err := suite.Index.WriteFile(*indexPath); err != nil {
		fmt.Fprintf(stderr, "demo: fingerprint index: %v\n", err)
		return pipeline.ExitBadInput
	}

	out := summary{
		Segments:            suite.Doc.Segments,
		SilenceMs:           suite.Doc.SilenceMs,
		Duplicates:          suite.Doc.Duplicates,
		Collisions:          suite.Doc.Collisions,
		TargetBitrateBps:    suite.Doc.TargetBitrateBps,
		ActualBitrateBps:    suite.Doc.ActualBitrateBps,
		BitrateDeviationPct: suite.Doc.BitrateDeviationPct,
		QualitySamples:      suite.Doc.QualitySamples,
		AlignmentErrorMs:    suite.Doc.AlignmentErrorMs,
		RecoveredTracks:     suite.Doc.RecoveredTracks,
		IsolatedTracks:      suite.Doc.IsolatedTracks,
		ManifestSHA256:      hash,
		ManifestFile:        *manifestPath,
		IndexFile:           *indexPath,
		Note:                manifest.Note,
	}
	for _, p := range suite.Paths {
		out.Paths = append(out.Paths, pathSummary{Name: p.Name, ExitCode: p.ExitCode})
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "demo: %v\n", err)
		return pipeline.ExitBadInput
	}
	fmt.Fprintln(stdout, string(b))
	return pipeline.ExitOK
}
