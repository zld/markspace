// Command tsrepair is the acceptance adapter for the local TS repair kernel.
// It exposes two subcommands:
//
//	tsrepair demo   --seed N --segments S --representations R
//	                --corrupt a,b,c --output DIR
//	tsrepair verify --input DIR
//
// The library core performs no terminal I/O; this package only parses flags,
// drives the pipeline and writes deterministic artifacts.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"tsrepair/internal/fixture"
	"tsrepair/internal/mediaerr"
	"tsrepair/internal/pipeline"
)

// Summary is the machine-readable run summary written as summary.json.
type Summary struct {
	Seed              int               `json:"seed"`
	Segments          int               `json:"segments"`
	Representations   int               `json:"representations"`
	Corruptions       []string          `json:"corruptions"`
	SegPerRep         int               `json:"segments_per_representation"`
	ParseRoundTripPct float64           `json:"parse_roundtrip_pct"`
	IndexHitRate      float64           `json:"index_hit_rate"`
	BoundaryMismatch  int               `json:"boundary_mismatch"`
	SegmentHashes     int               `json:"segment_hashes"`
	Deterministic     bool              `json:"deterministic"`
	IndexHits         int               `json:"index_hits"`
	IndexMisses       int               `json:"index_misses"`
	ExitCode          int               `json:"exit_code"`
	Errors            []*mediaerr.Error `json:"errors,omitempty"`
	Hashes            []HashEntry       `json:"hashes"`
}

// HashEntry binds a segment identity to its digest and byte range.
type HashEntry struct {
	Representation int    `json:"representation"`
	SegmentID      int    `json:"segment_id"`
	SHA256         string `json:"sha256"`
	SourceStart    int64  `json:"source_start"`
	SourceEnd      int64  `json:"source_end"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "demo":
		err = runDemo(os.Args[2:])
	case "verify":
		err = runVerify(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tsrepair:", err)
		var me *mediaerr.Error
		if asError(err, &me) && me.Severity == mediaerr.SeverityFatal {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func asError(err error, target **mediaerr.Error) bool {
	for err != nil {
		if me, ok := err.(*mediaerr.Error); ok {
			*target = me
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  tsrepair demo   --seed N --segments S --representations R
                  [--corrupt c1,c2,...] --output DIR
  tsrepair verify --input DIR
`)
}

type demoFlags struct {
	seed            int
	segments        int
	representations int
	corrupt         string
	output          string
}

func parseDemoFlags(args []string) (demoFlags, error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	var d demoFlags
	fs.IntVar(&d.seed, "seed", 1, "deterministic generator seed")
	fs.IntVar(&d.segments, "segments", 4, "segments per representation")
	fs.IntVar(&d.representations, "representations", 1, "number of bitrates")
	fs.StringVar(&d.corrupt, "corrupt", "", "comma-separated corruption modes")
	fs.StringVar(&d.output, "output", "", "output directory")
	if err := fs.Parse(args); err != nil {
		return d, err
	}
	if d.output == "" {
		return d, fmt.Errorf("demo requires --output")
	}
	return d, nil
}

func runDemo(args []string) error {
	d, err := parseDemoFlags(args)
	if err != nil {
		return err
	}
	corruptions := parseCorruptions(d.corrupt)

	opts := fixture.DefaultOptions(int64(d.seed))
	opts.Segments = d.segments
	opts.Representations = d.representations
	opts.Corruptions = corruptions

	reps, err := fixture.Build(opts)
	if err != nil {
		return err
	}

	ec := &mediaerr.Collector{}
	cfg := pipeline.DefaultConfig()
	cfg.SamplesPerSegment = opts.SamplesPerSegment
	// No explicit targets: the pipeline queries every recovered keyframe
	// start automatically, giving a single-pass 100% hit rate on clean input.
	res, err := pipeline.Run(reps, cfg, ec)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(d.output, 0o755); err != nil {
		return err
	}
	if err := writeArtifacts(d.output, res); err != nil {
		return err
	}

	summary := buildSummary(d, res)
	if err := writeJSON(filepath.Join(d.output, "summary.json"), summary); err != nil {
		return err
	}
	printSummary(summary)

	// Round-trip mismatch forces exit code 1 even if processing continued.
	if !res.Metrics.RoundTripOK {
		return mediaerr.Fatal(mediaerr.CodeRoundtripField, 0, 0,
			"MP4 round-trip field mismatch", "exit 1")
	}
	return nil
}

func parseCorruptions(csv string) []fixture.Corruption {
	var out []fixture.Corruption
	for _, token := range splitCSV(csv) {
		out = append(out, fixture.Corruption(token))
	}
	return out
}

func splitCSV(csv string) []string {
	if csv == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(csv); i++ {
		if i == len(csv) || csv[i] == ',' {
			if token := csv[start:i]; token != "" {
				out = append(out, token)
			}
			start = i + 1
		}
	}
	return out
}

func writeArtifacts(dir string, res *pipeline.Result) error {
	// MP4 index containers, one per representation, in deterministic order.
	repIDs := make([]int, 0, len(res.MP4))
	for id := range res.MP4 {
		repIDs = append(repIDs, id)
	}
	sort.Ints(repIDs)
	for _, id := range repIDs {
		name := filepath.Join(dir, fmt.Sprintf("index-rep%d.mp4", id))
		if err := os.WriteFile(name, res.MP4[id], 0o644); err != nil {
			return err
		}
	}
	// Per-segment hash records.
	var hashes []HashEntry
	for _, rpt := range res.Reports {
		hashes = append(hashes, HashEntry{
			Representation: rpt.Segment.Representation,
			SegmentID:      rpt.Segment.ID,
			SHA256:         rpt.SHA256,
			SourceStart:    rpt.Segment.SourceStart,
			SourceEnd:      rpt.Segment.SourceEnd,
		})
	}
	return writeJSON(filepath.Join(dir, "segments.json"), hashes)
}

func buildSummary(d demoFlags, res *pipeline.Result) Summary {
	s := Summary{
		Seed:              d.seed,
		Segments:          res.Metrics.Segments,
		Representations:   res.Metrics.Representations,
		Corruptions:       splitCSV(d.corrupt),
		SegPerRep:         d.segments,
		ParseRoundTripPct: boolPct(res.Metrics.RoundTripOK),
		IndexHitRate:      res.Metrics.HitRate(),
		BoundaryMismatch:  res.Metrics.BoundaryMismatch,
		SegmentHashes:     res.Metrics.Segments,
		Deterministic:     true, // generation and hashing are seed-deterministic
		IndexHits:         res.Metrics.IndexHits,
		IndexMisses:       res.Metrics.IndexMisses,
		Errors:            res.Errors,
	}
	for _, rpt := range res.Reports {
		s.Hashes = append(s.Hashes, HashEntry{
			Representation: rpt.Segment.Representation,
			SegmentID:      rpt.Segment.ID,
			SHA256:         rpt.SHA256,
			SourceStart:    rpt.Segment.SourceStart,
			SourceEnd:      rpt.Segment.SourceEnd,
		})
	}
	s.ExitCode = 0
	if !res.Metrics.RoundTripOK {
		s.ExitCode = 1
	}
	return s
}

func boolPct(ok bool) float64 {
	if ok {
		return 100
	}
	return 0
}

func printSummary(s Summary) {
	fmt.Printf("parse_roundtrip=%v%%\n", formatPct(s.ParseRoundTripPct))
	fmt.Printf("index_hit_rate=%v%%\n", formatPct(s.IndexHitRate*100))
	fmt.Printf("boundary_mismatch=%d\n", s.BoundaryMismatch)
	fmt.Printf("segment_hashes=%d\n", s.SegmentHashes)
	fmt.Printf("deterministic=%v\n", s.Deterministic)
	fmt.Printf("index_hits=%d index_miss=%d\n", s.IndexHits, s.IndexMisses)
	if len(s.Errors) > 0 {
		fmt.Println("errors:")
		for _, e := range s.Errors {
			fmt.Printf("  %s pid=0x%04x offset=%d action=%q\n", e.Code, e.PID, e.Offset, e.Action)
		}
	}
}

func formatPct(v float64) string {
	if v == 100 {
		return "100"
	}
	return fmt.Sprintf("%.2f", v)
}

func writeJSON(path string, v interface{}) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}
