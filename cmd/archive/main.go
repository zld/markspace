// Command archive builds auditable archive manifests from old tape
// digitizations: MP4 structure, subtitle cues, RTP continuity and damage
// ranges. It is a terminal-only tool: flags in, structured summary out.
package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"mediaarchive/internal/diag"
	"mediaarchive/internal/fixtures"
	"mediaarchive/internal/pipeline"
)

const usage = `archive - auditable media archive indexer

usage:
  archive analyze [flags]
  archive generate --dir DIR [--fixture SET]
  archive version

analyze flags:
  --fixture NAME   generated fixture set to create under --workdir (default none)
  --workdir DIR    directory for generated fixtures and outputs (default ./tmp/archive)
  --input PATH     analyze an explicit directory or .mp4/.srt/.vtt/.rtp file
                   (repeatable, read-only)
  --fps RATE       frame rate, e.g. 30000/1001 or 25 (default 30000/1001)
  --mode MODE      recover (default) or strict
  --workers N      bounded worker count (default 4)
  --queue N        bounded queue depth (default 8)
  --manifest PATH  atomically write the JSON manifest to PATH
  --csv PATH       atomically write manifest rows as CSV to PATH
  --json           print the manifest as JSON instead of the summary

exit codes: 0 complete, 1 media/processing error, 2 bad arguments
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "archive %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
		return 0
	case "generate":
		return runGenerate(args[1:], stdout, stderr)
	case "analyze":
		return runAnalyze(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

const version = "0.1.0"

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func runGenerate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", filepath.Join("tmp", "fixtures"), "output directory")
	which := fs.String("fixture", fixtures.SetAll, "fixture set name")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	set, err := fixtures.Generate(*dir, *which)
	if err != nil {
		fmt.Fprintf(stderr, "generate: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "fixture_set=%s assets=%d dir=%s\n", *which, len(set.Assets), set.Dir)
	for _, a := range set.Assets {
		fmt.Fprintf(stdout, "  asset=%s tag=%s fps=%s\n", a.Name, a.Tag, a.FPS)
	}
	return 0
}

func runAnalyze(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fixture := fs.String("fixture", "", "generated fixture set (e.g. generated, damaged)")
	workdir := fs.String("workdir", filepath.Join("tmp", "archive"), "fixture/output directory")
	fps := fs.String("fps", "30000/1001", "frame rate")
	modeStr := fs.String("mode", "recover", "recover or strict")
	workers := fs.Int("workers", 4, "worker count")
	queue := fs.Int("queue", 8, "queue depth")
	manifestPath := fs.String("manifest", "", "atomic JSON manifest output path")
	csvPath := fs.String("csv", "", "atomic CSV row export path")
	asJSON := fs.Bool("json", false, "print JSON manifest")
	var inputs multiFlag
	fs.Var(&inputs, "input", "explicit input path (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	mode, err := pipeline.ParseMode(*modeStr)
	if err != nil {
		fmt.Fprintf(stderr, "analyze: %v\n", err)
		return 2
	}
	if *workers < 1 || *queue < 1 {
		fmt.Fprintf(stderr, "analyze: --workers and --queue must be >= 1\n")
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "analyze: unexpected arguments %v\n", fs.Args())
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	assets, err := collectAssets(*fixture, *workdir, *fps, inputs)
	if err != nil {
		fmt.Fprintf(stderr, "analyze: %v\n", err)
		// An unknown fixture name or missing input path is a parameter
		// problem, not damaged media, so it must return the argument code.
		return 2
	}
	if len(assets) == 0 {
		fmt.Fprintf(stderr, "analyze: no inputs (use --fixture or --input)\n")
		return 2
	}

	m, err := pipeline.Run(ctx, pipeline.Options{Mode: mode, Workers: *workers, Queue: *queue, FPS: *fps}, assets)
	if err != nil {
		reportError(stderr, err)
		if mode == pipeline.ModeStrict || len(m.Faults) > 0 {
			return 1
		}
		return 1
	}
	if *asJSON || *manifestPath != "" {
		data, jerr := json.MarshalIndent(m, "", "  ")
		if jerr != nil {
			fmt.Fprintf(stderr, "analyze: encode manifest: %v\n", jerr)
			return 1
		}
		data = append(data, '\n')
		if *manifestPath != "" {
			if werr := atomicWrite(*manifestPath, data); werr != nil {
				fmt.Fprintf(stderr, "analyze: write manifest: %v\n", werr)
				return 1
			}
		}
		if *asJSON {
			stdout.Write(data)
		}
	}
	if *csvPath != "" {
		if err := writeCSV(*csvPath, m); err != nil {
			fmt.Fprintf(stderr, "analyze: write csv: %v\n", err)
			return 1
		}
	}
	printSummary(stdout, m)
	return 0
}

func reportError(w io.Writer, err error) {
	if f, ok := diag.As(err); ok {
		fmt.Fprintf(w, "error code=%s offset=%d", f.Code, f.Offset)
		if f.HasMedia {
			fmt.Fprintf(w, " media_ms=%.3f", f.MediaMs)
		}
		fmt.Fprintf(w, " detail=%s\n", f.Detail)
		return
	}
	fmt.Fprintf(w, "error %v\n", err)
}

func collectAssets(fixture, workdir, fps string, inputs []string) ([]pipeline.AssetInput, error) {
	var assets []pipeline.AssetInput
	if fixture != "" {
		set, err := fixtures.Generate(workdir, fixture)
		if err != nil {
			return nil, fmt.Errorf("generate fixture %q: %w", fixture, err)
		}
		for _, a := range set.Assets {
			ra := pipeline.AssetInput{
				Asset: a.Name, MP4: a.MP4, Subtitle: a.Subtitle, RTP: a.RTP,
				FPS: a.FPS, TargetBps: a.TargetBps, LabelFormat: a.LabelFormat,
			}
			if ra.FPS == "" {
				ra.FPS = fps
			}
			assets = append(assets, ra)
		}
	}
	grouped := map[string]*pipeline.AssetInput{}
	var order []string
	push := func(name string) *pipeline.AssetInput {
		if a, ok := grouped[name]; ok {
			return a
		}
		a := &pipeline.AssetInput{Asset: name, FPS: fps}
		grouped[name] = a
		order = append(order, name)
		return a
	}
	for _, in := range inputs {
		info, err := os.Stat(in)
		if err != nil {
			return nil, fmt.Errorf("input %s: %w", in, err)
		}
		if info.IsDir() {
			entries, err := os.ReadDir(in)
			if err != nil {
				return nil, err
			}
			for _, e := range entries {
				if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
					continue
				}
				attach(push(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))), filepath.Join(in, e.Name()))
			}
			continue
		}
		attach(push(strings.TrimSuffix(filepath.Base(in), filepath.Ext(in))), in)
	}
	for _, name := range order {
		assets = append(assets, *grouped[name])
	}
	return assets, nil
}

func attach(a *pipeline.AssetInput, path string) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v", ".mov":
		a.MP4 = path
	case ".srt", ".vtt":
		a.Subtitle = path
	case ".rtp", ".pcap-rtp", ".log":
		a.RTP = path
	}
}

// writeCSV exports manifest rows with encoding/csv so downstream catalogues
// can import them without parsing the human summary. Rows are already in
// deterministic total order, and the header is fixed.
func writeCSV(path string, m *pipeline.Manifest) error {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write([]string{
		"asset", "start_ms", "start_us", "seq", "stage", "kind", "payload",
	}); err != nil {
		return err
	}
	for _, r := range m.Rows {
		rec := []string{
			r.Asset,
			strconv.FormatFloat(r.StartMs, 'f', 3, 64),
			strconv.FormatInt(r.StartUs, 10),
			strconv.FormatInt(r.Seq, 10),
			r.Stage,
			r.Kind,
			r.Payload,
		}
		if err := w.Write(rec); err != nil {
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	return atomicWrite(path, buf.Bytes())
}

// atomicWrite writes through a temp file in the destination directory and
// renames it into place, so a crash never leaves a partial manifest.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".manifest-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func printSummary(w io.Writer, m *pipeline.Manifest) {
	fmt.Fprintf(w, "mode=%s assets=%d rows=%d cues=%d damage=%d\n",
		m.Mode, m.Assets, len(m.Rows), m.Metrics.CueCount, m.Metrics.DamageCount)
	fmt.Fprintf(w, "target_bitrate_bps=%d\n", m.Metrics.TargetBitrateBps)
	fmt.Fprintf(w, "actual_bitrate_bps=%d\n", m.Metrics.ActualBitrateBps)
	fmt.Fprintf(w, "bitrate_deviation_pct=%.3f\n", m.Metrics.DeviationPct)
	fmt.Fprintf(w, "cue_count=%d\n", m.Metrics.CueCount)
	fmt.Fprintf(w, "damage_count=%d\n", m.Metrics.DamageCount)
	fmt.Fprintf(w, "keyframe_gap_max_ms=%.3f\n", m.Metrics.KeyframeGapMaxMs)
	fmt.Fprintf(w, "drift_ppm=%.3f\n", m.Metrics.DriftPPM)
	fmt.Fprintf(w, "manifest_alignment_error_ms=%.6f\n", m.Metrics.AlignmentErrorMs)
	fmt.Fprintf(w, "manifest_sha256=%s\n", m.Digest)
	for _, s := range m.Segments {
		fmt.Fprintf(w, "damage source=%s code=%s start_offset=%d end_offset=%d fault_count=%d recoverable=%t\n",
			s.Source, s.Code, s.StartOffset, s.EndOffset, s.FaultCount, s.Recoverable)
	}
	fmt.Fprintf(w, "assets=%d rows=%d keyframes=%d samples=%d media_duration_ms=%.3f\n",
		m.Assets, len(m.Rows), m.Metrics.KeyframeCount, m.Metrics.SampleCount, m.Metrics.MediaDurationMs)
	for _, a := range m.AssetDump {
		drift := "n/a"
		if a.HasDrift {
			drift = fmt.Sprintf("%.3f", a.DriftPPM)
		}
		fmt.Fprintf(w, "asset=%s fps=%s keyframes=%d samples=%d cues=%d duration_ms=%.3f drift_ppm=%s damage=%d alignment_error_ms=%.6f\n",
			a.Asset, a.FPS, a.Keyframes, a.Samples, a.Cues, a.DurationMs, drift, a.Damage, a.AlignmentError)
	}
	for _, f := range m.Faults {
		fmt.Fprintf(w, "fault code=%s offset=%d detail=%s\n", f.Code, f.Offset, f.Detail)
	}
}
