package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"mediarc/internal/fixture"
	"mediarc/internal/model"
	"mediarc/internal/pipeline"
	"mediarc/internal/track"
	"mediarc/internal/verify"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mediarc <demo|verify> [flags]")
		return 64
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "-h", "--help", "help":
		printUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", args[0])
		printUsage()
		return 64
	}
}

func printUsage() {
	fmt.Println(`mediarc - deterministic media archive recovery kernel

Commands:
  demo   --seed N --loss N --late-ms MS --out FILE [--map 1:1,2:2]
  verify --case truncated|late-boundary|color-conflict|loss-over-window`)
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	seed := fs.Int64("seed", 7, "deterministic fixture seed")
	loss := fs.Int("loss", 8, "number of injected RTP packet losses")
	lateMS := fs.Int64("late-ms", 120, "lateness of the metadata event in milliseconds")
	out := fs.String("out", "", "output MP4 path; must not already exist")
	mapSpec := fs.String("map", "", "optional explicit source:target track mapping")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "--out is required")
		return 64
	}
	if _, err := os.Lstat(*out); err == nil {
		me := model.NewError(model.StageCLI, model.CodeOutputExists, "refusing to overwrite existing output", nil)
		fmt.Fprintln(os.Stderr, me.Error())
		return 2
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "cannot inspect output: %v\n", err)
		return 1
	}
	if *mapSpec != "" {
		if _, err := track.ParseSpec(*mapSpec); err != nil {
			printMediaError(err)
			return 2
		}
	}

	opt := fixture.Options{Seed: *seed, LossPackets: *loss, LateMS: *lateMS}
	oc, err := pipeline.RunDemoWithMap(opt, *mapSpec)
	if err != nil {
		printMediaError(err)
		return 1
	}

	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		me := model.NewError(model.StageCLI, model.CodeOutputExists, "cannot create output without overwrite", err)
		printMediaError(me)
		return 2
	}
	if _, err := f.Write(oc.Remux.Bytes); err != nil {
		_ = f.Close()
		_ = os.Remove(*out)
		printMediaError(model.NewError(model.StageRemux, model.CodeInvalidArgument, "write output failed", err))
		return 1
	}
	if err := f.Close(); err != nil {
		printMediaError(model.NewError(model.StageRemux, model.CodeInvalidArgument, "close output failed", err))
		return 1
	}

	printDemoReport(*out, oc)
	if !verify.AcceptanceOK(oc.Report) {
		fmt.Fprintln(os.Stderr, "status=threshold_failed")
		return 1
	}
	return 0
}

func printDemoReport(out string, oc *pipeline.Outcome) {
	r := oc.Report
	fmt.Println("status=ok")
	fmt.Printf("output=%s\n", out)
	fmt.Printf("av_sync_ms=%d\n", r.AVSyncMS)
	fmt.Printf("packet_recovery_rate=%.3f\n", r.PacketRecoveryRate)
	fmt.Printf("p95_latency_ms=%d\n", r.P95LatencyMS)
	fmt.Printf("input_sample_count=%d\n", r.InputSampleCount)
	fmt.Printf("output_sample_count=%d\n", r.OutputSampleCount)
	fmt.Printf("sample_hashes_match=%t\n", r.SampleHashesMatch)
	fmt.Printf("properties_match=%t\n", r.PropertiesMatch)
	fmt.Printf("timeline_rewind_count=%d\n", r.RewindCount)
	fmt.Printf("rtp_injected_lost=%d rtp_recovered=%d rtp_reordered=%d rtp_duplicates=%d\n",
		r.InjectedLossPackets, r.RecoveredPackets, r.ReorderedPackets, r.DuplicatePackets)
	fmt.Printf("output_sha256=%s\n", r.DeterministicHash)
	fmt.Println("input_track_summaries:")
	for _, t := range r.InputTracks {
		fmt.Printf("  input id=%d type=%s codec=%s samples=%d timescale=%s rate=%d layout=%s channels=%d color=%s hash=%s\n",
			t.ID, t.Type, t.Codec, t.Samples, t.TimeBase, t.SampleRate, t.Layout, t.Channels, colorString(t.Color), shortHash(t.SampleHash))
	}
	fmt.Println("output_track_summaries:")
	for _, t := range r.OutputTracks {
		fmt.Printf("  output id=%d type=%s codec=%s samples=%d timescale=%s rate=%d layout=%s channels=%d color=%s hash=%s\n",
			t.ID, t.Type, t.Codec, t.Samples, t.TimeBase, t.SampleRate, t.Layout, t.Channels, colorString(t.Color), shortHash(t.SampleHash))
	}
}

func colorString(c *model.ColorMeta) string {
	if c == nil {
		return "absent"
	}
	return fmt.Sprintf("p%d/t%d/m%d/range=%t", c.Primaries, c.Transfer, c.Matrix, c.FullRange)
}

func shortHash(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}

func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	caseName := fs.String("case", "", "verification case name")
	jsonOut := fs.Bool("json", false, "emit full result as JSON")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if *caseName == "" {
		fmt.Fprintln(os.Stderr, "--case is required")
		return 64
	}
	res, err := pipeline.RunCase(*caseName)
	if res != nil && *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
	}
	if res != nil {
		printCaseReport(res)
	}
	if err != nil {
		printMediaError(err)
		return 2
	}
	fmt.Println("status=ok")
	return 0
}

func printCaseReport(res *pipeline.CaseResult) {
	fmt.Printf("case=%s status=error\n", res.Name)
	if res.Offset != nil {
		fmt.Printf("source_offset=%d\n", *res.Offset)
	}
	if res.RewindCount != 0 || res.Name == "late-boundary" {
		fmt.Printf("rewind_count=%d\n", res.RewindCount)
	}
	if len(res.ConflictFields) > 0 || res.Name == "color-conflict" {
		fmt.Printf("conflict_fields=[%s]\n", strings.Join(res.ConflictFields, ","))
	}
	if res.Unrecoverable != 0 || res.Name == "loss-over-window" {
		fmt.Printf("unrecoverable_packets=%d\n", res.Unrecoverable)
	}
}

func printMediaError(err error) {
	me, ok := model.AsMediaError(err)
	if !ok {
		fmt.Fprintf(os.Stderr, "error=%v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "error stage=%s code=%s message=%q", me.Stage, me.Code, me.Message)
	if me.TrackID != nil {
		fmt.Fprintf(os.Stderr, " track=%d", *me.TrackID)
	}
	if me.SourceOffset != nil {
		fmt.Fprintf(os.Stderr, " source_offset=%d", *me.SourceOffset)
	}
	if me.Timestamp != nil {
		fmt.Fprintf(os.Stderr, " pts_ms=%d", me.Timestamp.Millis())
	}
	if len(me.Fields) > 0 {
		fmt.Fprintf(os.Stderr, " conflict_fields=[%s]", strings.Join(me.Fields, ","))
	}
	if me.Unrecoverable > 0 {
		fmt.Fprintf(os.Stderr, " unrecoverable_packets=%d", me.Unrecoverable)
	}
	if me.Err != nil {
		fmt.Fprintf(os.Stderr, " cause=%q", me.Err.Error())
	}
	fmt.Fprintln(os.Stderr)
}
