// Command mediasync is the offline media-synchronization kernel CLI.
//
// Usage:
//
//	mediasync demo   --out DIR [--interrupt-after N]
//	mediasync resume --dir DIR
//
// All work is local: fixtures are synthesized, no network or devices.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediasync/internal/fixture"
	"mediasync/internal/index"
	"mediasync/internal/model"
	"mediasync/internal/rtp"
	"mediasync/internal/runner"
)

const (
	exitOK       = 0
	exitFailure  = 1
	exitUsage    = 2
	manifestName = "manifest.json"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitUsage)
	}
	var err error
	switch os.Args[1] {
	case "demo":
		err = runDemo(os.Args[2:])
	case "resume":
		err = runResume(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		os.Exit(exitOK)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(exitUsage)
	}
	if err != nil {
		if err == runner.InterruptError {
			fmt.Fprintln(os.Stderr, "interrupted: checkpoint saved, run 'resume' to continue")
			os.Exit(exitOK)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		if model.AsMediaError(err) != nil {
			os.Exit(exitFailure)
		}
		os.Exit(exitUsage)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `mediasync - offline media synchronization kernel

  mediasync demo   --out DIR [--interrupt-after N]
      Generate normal, corrupt and boundary fixtures and process them.
  mediasync resume --dir DIR
      Resume the demo task from its last complete checkpoint.
`)
}

type flagSet struct {
	values map[string]string
	bools  map[string]bool
}

func parseFlags(args []string, allowed map[string]bool) (*flagSet, error) {
	fs := &flagSet{values: map[string]string{}, bools: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			return nil, fmt.Errorf("unexpected argument %q", a)
		}
		name := strings.TrimPrefix(a, "--")
		if !allowed[name] {
			return nil, fmt.Errorf("unknown flag --%s", name)
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			fs.values[name] = args[i+1]
			i++
		} else {
			fs.bools[name] = true
		}
	}
	return fs, nil
}

// demoLayout describes one generated scenario directory.
type demoLayout struct {
	name      string
	media     string
	events    string
	params    runner.Params
	scenario  fixture.RTPScenario
	corrupt   bool
	truncated bool
}

func runDemo(args []string) error {
	fs, err := parseFlags(args, map[string]bool{"out": true, "interrupt-after": true})
	if err != nil {
		os.Exit(exitUsage)
		return err
	}
	out := fs.values["out"]
	if out == "" {
		fmt.Fprintln(os.Stderr, "demo requires --out DIR")
		os.Exit(exitUsage)
	}
	interruptAfter := 0
	if v := fs.values["interrupt-after"]; v != "" {
		if _, err := fmt.Sscanf(v, "%d", &interruptAfter); err != nil || interruptAfter < 0 {
			fmt.Fprintln(os.Stderr, "--interrupt-after must be a non-negative integer")
			os.Exit(exitUsage)
		}
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	layouts := buildLayouts(out)
	var firstFailure error
	for _, l := range layouts {
		if err := os.MkdirAll(filepath.Dir(l.media), 0o755); err != nil {
			return err
		}
		generateFixture(l)
	}

	// Each scenario is independent; the normal scenario supports resume,
	// while corrupt/boundary cases are one-shot analyses reported alongside.
	normal := layouts[0]
	interrupted := false
	if err := processScenario(out, normal, interruptAfter); err != nil {
		if err == runner.InterruptError {
			interrupted = true
		} else {
			firstFailure = err
		}
	}
	for _, l := range layouts[1:] {
		if err := processScenario(out, l, 0); err != nil {
			fmt.Fprintf(os.Stderr, "[%s] %v\n", l.name, err)
			// During an interrupted run the remaining scenarios are best-effort
			// diagnostics: their structured errors are reported with byte offsets
			// but do not fail the command, which resumes the normal task later.
			if !interrupted && firstFailure == nil {
				firstFailure = err
			}
		}
	}
	if interrupted {
		fmt.Fprintln(os.Stderr, "interrupted: checkpoint saved, run 'resume' to continue")
		return nil
	}
	if firstFailure != nil {
		return firstFailure
	}
	return nil
}

func buildLayouts(out string) []demoLayout {
	base := runner.DefaultParams()
	clean := demoLayout{
		name:   "normal",
		media:  filepath.Join(out, "normal", "scene.mp4"),
		events: filepath.Join(out, "normal", "events.jsonl"),
		params: base,
		scenario: fixture.RTPScenario{
			SSRC: base.SSRC, ClockHz: base.ClockHz, PacketCount: 40, StartSeq: 100,
		},
	}
	lossParams := base
	lossParams.InjectedLoss = 4
	loss := demoLayout{
		name:   "loss",
		media:  filepath.Join(out, "loss", "scene.mp4"),
		events: filepath.Join(out, "loss", "events.jsonl"),
		params: lossParams,
		scenario: fixture.RTPScenario{
			SSRC: base.SSRC, ClockHz: base.ClockHz, PacketCount: 40, StartSeq: 100,
			LateSeqs: map[uint16]bool{102: true, 103: true}, LateBy: 3,
			DropSeqs: map[uint16]bool{110: true, 111: true},
		},
	}
	boundaryParams := base
	boundaryParams.ReorderWindow = model.MaxReorderWindow
	boundary := demoLayout{
		name:   "boundary",
		media:  filepath.Join(out, "boundary", "scene.mp4"),
		events: filepath.Join(out, "boundary", "events.jsonl"),
		params: boundaryParams,
		scenario: fixture.RTPScenario{
			SSRC: base.SSRC, ClockHz: base.ClockHz, PacketCount: 20,
			StartSeq: 0xFFF0, Wrap: true,
			DupSeqs: map[uint16]bool{0xFFF5: true},
			Reorder: true,
		},
	}
	corrupt := demoLayout{
		name:      "corrupt",
		media:     filepath.Join(out, "corrupt", "scene.mp4"),
		events:    filepath.Join(out, "corrupt", "events.jsonl"),
		params:    base,
		corrupt:   true,
		truncated: true,
		scenario:  fixture.RTPScenario{SSRC: base.SSRC, ClockHz: base.ClockHz, PacketCount: 10},
	}
	return []demoLayout{clean, loss, boundary, corrupt}
}

func generateFixture(l demoLayout) {
	specs := []fixture.TrackSpec{
		{Handler: "soun", TrackID: 1, Timescale: 1000,
			Deltas: repeatU32(100, 8)},
		{Handler: "subt", TrackID: 2, Timescale: 1000,
			Deltas: repeatU32(100, 8)},
	}
	data, _ := fixture.WriteMP4(l.media, 1000, specs)
	if l.truncated {
		_ = fixture.Truncate(l.media, int64(len(data))-32)
	}
	stream := fixture.BuildRTP(l.scenario)
	arrivals := make([]model.Ticks, len(stream.Arrivals))
	for i, a := range stream.Arrivals {
		arrivals[i] = model.Ticks(a)
	}
	_ = rtp.WriteEventLog(l.events, arrivals, stream.Datagrams)
}

func processScenario(out string, l demoLayout, interruptAfter int) error {
	// Each scenario gets its own task directory so checkpoints, indexes and
	// results never collide across different inputs.
	runDir := filepath.Join(out, l.name, "run")
	res, err := index.Scan(l.media, l.params.Limits)
	if err != nil {
		return err // structured error with offset; caller exits non-zero
	}
	pl, err := runner.NewPipeline(runDir, l.params, res.InputSHA256)
	if err != nil {
		return err
	}
	if interruptAfter > 0 {
		pl.SetInterrupt(interruptAfter)
	}
	result, manifest, err := pl.Run(l.media, l.events, nil)
	if err != nil {
		return err
	}
	if _, err := runner.WriteResult(runDir, result); err != nil {
		return err
	}
	// The embedded content digest must independently re-verify from disk;
	// the whole-file digest is what the manifest records.
	if err := runner.VerifyResult(filepath.Join(runDir, runner.ResultName), result.ResultSHA256); err != nil {
		return err
	}
	if err := manifest.AttachResult(runDir, runner.ResultName); err != nil {
		return err
	}
	if _, err := manifest.WriteAtomic(runDir, manifestName); err != nil {
		return err
	}
	printSummary(l.name, result)
	return nil
}

func runResume(args []string) error {
	fs, err := parseFlags(args, map[string]bool{"dir": true})
	if err != nil {
		os.Exit(exitUsage)
		return err
	}
	dir := fs.values["dir"]
	if dir == "" {
		fmt.Fprintln(os.Stderr, "resume requires --dir DIR")
		os.Exit(exitUsage)
	}
	runDir := filepath.Join(dir, "normal", "run")
	params := runner.DefaultParams()
	media := filepath.Join(dir, "normal", "scene.mp4")
	events := filepath.Join(dir, "normal", "events.jsonl")

	// Resume derives identity from the current input, not from a manifest: an
	// interrupted run has no manifest yet. The pipeline's checkpoint Guard then
	// rejects any state whose recorded input digest no longer matches.
	scan, err := index.Scan(media, params.Limits)
	if err != nil {
		return err
	}
	pl, err := runner.NewPipeline(runDir, params, scan.InputSHA256)
	if err != nil {
		return err
	}
	result, manifest, err := pl.Run(media, events, nil)
	if err != nil {
		return err
	}
	if _, err := runner.WriteResult(runDir, result); err != nil {
		return err
	}
	if err := runner.VerifyResult(filepath.Join(runDir, runner.ResultName), result.ResultSHA256); err != nil {
		return err
	}
	if err := manifest.AttachResult(runDir, runner.ResultName); err != nil {
		return err
	}
	if _, err := manifest.WriteAtomic(runDir, manifestName); err != nil {
		return err
	}
	printSummary("resume", result)
	return nil
}

func printSummary(name string, r *runner.Result) {
	fmt.Printf("=== %s ===\n", name)
	keys := []struct {
		label string
		value any
	}{
		{"sync_error_ns", r.SyncErrorNS},
		{"loss_recovery_rate", r.LossRecovery},
		{"p95_latency_ms", r.P95LatencyMS},
		{"duplicates", r.Duplicates},
		{"missing", r.Missing},
		{"input_sha256", r.InputSHA256},
		{"index_sha256", r.IndexSHA256},
		{"result_sha256", r.ResultSHA256},
	}
	for _, k := range keys {
		fmt.Printf("%s=%v\n", k.label, k.value)
	}
	raw, _ := json.Marshal(r.Errors)
	fmt.Printf("errors=%s\n", string(raw))
}

func repeatU32(v uint32, n int) []uint32 {
	out := make([]uint32, n)
	for i := range out {
		out[i] = v
	}
	return out
}
