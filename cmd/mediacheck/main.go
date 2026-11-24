// Command mediacheck is the terminal adapter for the reusable media
// processing kernel. It generates deterministic local fixtures (demo) or
// scans a local MP4/MOV file, and prints the quantitative summary as JSON
// or human-readable text. Exit codes: 0 success, 2 unparseable input,
// 3 unrecoverable damage present.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"mediacheck/internal/container"
	"mediacheck/internal/fixture"
	"mediacheck/internal/pipeline"
)

// Exit codes per the kernel contract.
const (
	exitOK            = 0
	exitUnparseable   = 2
	exitUnrecoverable = 3
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitOK
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "scan":
		return runScan(args[1:])
	case "-h", "--help", "help":
		usage()
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage()
		return exitUnparseable
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `mediacheck - local MP4/MOV and RTP media kernel

Usage:
  mediacheck demo --case <name|all> --seed <int> [--json]
  mediacheck scan --file <path> [--json]

Cases:
  normal, wrap_drift, vfr, truncated, cross_track, rtp_loss, all
Exit codes: 0 ok, 2 unparseable input, 3 unrecoverable damage
`)
}

// demoOutput is the top-level JSON envelope; field names match the
// acceptance contract exactly.
type demoOutput struct {
	Seed             int64            `json:"seed"`
	Cases            []caseOutput     `json:"cases"`
	Tracks           []trackLine      `json:"tracks"`
	KeyframeGapMs    map[uint32]int64 `json:"keyframe_gap_ms"`
	DamageRanges     int              `json:"damage_ranges"`
	RecoveredPackets int              `json:"recovered_packets"`
	LossRecoveryRate float64          `json:"loss_recovery_rate"`
	AVSyncErrorMs    float64          `json:"av_sync_error_ms"`
	P95LatencyMs     int64            `json:"p95_latency_ms"`
}

type caseOutput struct {
	Case             fixture.CaseName `json:"case"`
	Description      string           `json:"description"`
	Tracks           []trackLine      `json:"tracks"`
	KeyframeGapMs    map[uint32]int64 `json:"keyframe_gap_ms"`
	DamageRanges     int              `json:"damage_ranges"`
	RecoveredPackets int              `json:"recovered_packets"`
	LossRecoveryRate float64          `json:"loss_recovery_rate"`
	AVSyncErrorMs    float64          `json:"av_sync_error_ms"`
	P95LatencyMs     int64            `json:"p95_latency_ms"`
	HasUnrecoverable bool             `json:"has_unrecoverable"`
}

type trackLine struct {
	TrackID   uint32 `json:"track_id"`
	Kind      string `json:"kind"`
	Samples   int    `json:"samples"`
	Monotonic bool   `json:"monotonic"`
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	caseName := fs.String("case", "all", "case name or 'all'")
	seed := fs.Int64("seed", 23, "deterministic fixture seed")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return exitUnparseable
	}
	cases, err := selectCases(*caseName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo: %v\n", err)
		return exitUnparseable
	}
	work, err := os.MkdirTemp("", "mediacheck-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo: %v\n", err)
		return exitUnparseable
	}
	defer os.RemoveAll(work)

	envelope := demoOutput{Seed: *seed, KeyframeGapMs: map[uint32]int64{}}
	var last *pipeline.Outcome
	for _, name := range cases {
		c := fixture.BuildCase(name, *seed)
		out, err := pipeline.Run(c, work)
		if err != nil {
			fmt.Fprintf(os.Stderr, "demo %s: %v\n", name, err)
			return exitUnparseable
		}
		last = out
		co := caseOutput{
			Case: name, Description: c.Description,
			KeyframeGapMs:    out.Metrics.KeyframeGapMs,
			DamageRanges:     len(out.Metrics.DamageRanges),
			RecoveredPackets: out.Metrics.RecoveredPackets,
			LossRecoveryRate: out.Metrics.LossRecoveryRate,
			AVSyncErrorMs:    out.Metrics.AVSyncErrorMs,
			P95LatencyMs:     out.Metrics.P95LatencyMs,
			HasUnrecoverable: out.Metrics.HasUnrecoverable,
		}
		for _, tr := range out.Metrics.Tracks {
			co.Tracks = append(co.Tracks, trackLine{
				TrackID: tr.TrackID, Kind: tr.Kind,
				Samples: tr.SampleCount, Monotonic: tr.Monotonic,
			})
		}
		envelope.Cases = append(envelope.Cases, co)
	}
	// Top-level convenience fields mirror the final (or only) case so the
	// exact acceptance keys are always present at envelope level too.
	if last != nil {
		envelope.Tracks = flattenTracks(last)
		envelope.KeyframeGapMs = last.Metrics.KeyframeGapMs
		envelope.DamageRanges = len(last.Metrics.DamageRanges)
		envelope.RecoveredPackets = last.Metrics.RecoveredPackets
		envelope.LossRecoveryRate = last.Metrics.LossRecoveryRate
		envelope.AVSyncErrorMs = last.Metrics.AVSyncErrorMs
		envelope.P95LatencyMs = last.Metrics.P95LatencyMs
	}
	if *asJSON {
		return writeJSON(envelope)
	}
	printText(envelope)
	return exitOK
}

func runScan(args []string) int {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	path := fs.String("file", "", "path to a local MP4/MOV file")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return exitUnparseable
	}
	if *path == "" {
		fmt.Fprintln(os.Stderr, "scan: --file is required")
		return exitUnparseable
	}
	res, err := container.Scan(*path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scan: %v\n", err)
		if errors.Is(err, container.ErrUnparseable) {
			return exitUnparseable
		}
		return exitUnparseable
	}
	type scanOut struct {
		Path        string `json:"path"`
		TrackCount  int    `json:"track_count"`
		Undecidable int    `json:"undecidable_tracks"`
		Truncations int    `json:"truncated_boxes"`
	}
	so := scanOut{Path: *path, TrackCount: len(res.Tracks), Truncations: len(res.Pre.Truncations)}
	for _, tm := range res.Tracks {
		if tm.Index.Undecidable {
			so.Undecidable++
		}
	}
	// Any undecidable index or truncated box is an unrecoverable region:
	// the kernel cannot reconstruct the missing sample data without
	// external input, so the contract exit code is 3.
	exitCode := exitOK
	if so.Truncations > 0 || so.Undecidable > 0 {
		exitCode = exitUnrecoverable
	}
	if *asJSON {
		if code := writeJSON(so); code != exitOK {
			return code
		}
		return exitCode
	}
	fmt.Printf("file=%s tracks=%d undecidable=%d truncated_boxes=%d\n",
		so.Path, so.TrackCount, so.Undecidable, so.Truncations)
	return exitCode
}

func selectCases(name string) ([]fixture.CaseName, error) {
	if name == "all" {
		return fixture.AllCases, nil
	}
	for _, c := range fixture.AllCases {
		if string(c) == name {
			return []fixture.CaseName{c}, nil
		}
	}
	return nil, fmt.Errorf("unknown case %q (valid: %s, all)", name,
		strings.Join(caseStrings(), ", "))
}

func caseStrings() []string {
	out := make([]string, len(fixture.AllCases))
	for i, c := range fixture.AllCases {
		out[i] = string(c)
	}
	return out
}

func flattenTracks(out *pipeline.Outcome) []trackLine {
	var tl []trackLine
	for _, tr := range out.Metrics.Tracks {
		tl = append(tl, trackLine{
			TrackID: tr.TrackID, Kind: tr.Kind,
			Samples: tr.SampleCount, Monotonic: tr.Monotonic,
		})
	}
	return tl
}

func writeJSON(v interface{}) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "encode: %v\n", err)
		return exitUnparseable
	}
	return exitOK
}

func printText(env demoOutput) {
	for _, c := range env.Cases {
		fmt.Printf("case=%s (%s)\n", c.Case, c.Description)
		fmt.Printf("  tracks=%d keyframe_gap_ms=%v damage_ranges=%d\n",
			len(c.Tracks), c.KeyframeGapMs, c.DamageRanges)
		fmt.Printf("  recovered_packets=%d loss_recovery_rate=%.2f av_sync_error_ms=%.2f p95_latency_ms=%d\n",
			c.RecoveredPackets, c.LossRecoveryRate, c.AVSyncErrorMs, c.P95LatencyMs)
	}
}
