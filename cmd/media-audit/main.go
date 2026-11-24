// Command media-audit is the terminal adapter for the media processing
// kernel. It generates fixtures, runs a batch audit, and prints the
// acceptance metrics and classified diagnostics.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mediaaudit/internal/fixtures"
	"mediaaudit/internal/model"
	"mediaaudit/internal/pipeline"
	"mediaaudit/internal/timecode"
)

// Exit codes are part of the contract.
const (
	exitOK          = 0
	exitFatal       = 1
	exitRecoverable = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type config struct {
	generate       bool
	samplesRoot    string
	outDir         string
	chunk          int
	resume         bool
	interruptAfter int
	interval       string
	width, height  int
	snapTolerance  string
	maxDriftPPM    int64
	noFallback     bool
	timecodeMode   string
	verify         bool
	quiet          bool
}

// normalizeArgs makes "-generate DIR" work even though -generate is a bool
// flag: the bare form stays a flag, the directory form becomes
// "-generate=true -samples DIR". Go's flag package otherwise stops at DIR and
// silently ignores every later flag.
func normalizeArgs(args []string) []string {
	out := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-generate" || a == "--generate" {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				out = append(out, "-generate=true", "-samples", args[i+1])
				i++
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

func parseFlags(args []string, errw *os.File) (config, error) {
	var c config
	fs := flag.NewFlagSet("media-audit", flag.ContinueOnError)
	fs.SetOutput(errw)
	fs.BoolVar(&c.generate, "generate", false, "generate the normal/edge/broken fixture sets before auditing (accepts an optional directory)")
	fs.StringVar(&c.samplesRoot, "samples", "", "fixture directory (default: <out>/samples)")
	fs.StringVar(&c.outDir, "out", "", "output directory for checkpoints and the report (required)")
	fs.IntVar(&c.chunk, "chunk", 32, "timeline items per committed chunk")
	fs.BoolVar(&c.resume, "resume", false, "continue a checkpointed batch instead of restarting")
	fs.IntVar(&c.interruptAfter, "interrupt-after", 0, "stop after committing N chunks in this run")
	fs.StringVar(&c.interval, "interval", "1/25", "frame sampling cadence in seconds")
	fs.IntVar(&c.width, "width", 16, "audit raster width")
	fs.IntVar(&c.height, "height", 16, "audit raster height")
	fs.StringVar(&c.snapTolerance, "snap", "0", "snap subtitle boundaries to video cuts within this many seconds")
	fs.Int64Var(&c.maxDriftPPM, "max-drift-ppm", 500, "per-track clock drift threshold; negative disables")
	fs.BoolVar(&c.noFallback, "no-fallback", false, "disable the MP4 keyframe scan fallback")
	fs.StringVar(&c.timecodeMode, "timecode-mode", "", "override the fixture timecode mode: 24, 25, 29.97ndf, 29.97df or 50")
	fs.BoolVar(&c.verify, "verify", false, "verify committed chunks without processing new work")
	fs.BoolVar(&c.quiet, "quiet", false, "suppress per-diagnostic lines")
	if err := fs.Parse(normalizeArgs(args)); err != nil {
		return c, err
	}
	if c.outDir == "" {
		return c, errors.New("-out is required")
	}
	if c.chunk <= 0 {
		return c, fmt.Errorf("-chunk must be positive, got %d", c.chunk)
	}
	if c.interval == "" {
		return c, errors.New("-interval must not be empty")
	}
	if _, err := model.ParseRat(c.interval); err != nil {
		return c, fmt.Errorf("-interval: %v", err)
	}
	if _, err := model.ParseRat(c.snapTolerance); err != nil {
		return c, fmt.Errorf("-snap: %v", err)
	}
	if c.width <= 0 || c.height <= 0 {
		return c, errors.New("-width and -height must be positive")
	}
	if c.interruptAfter < 0 {
		return c, errors.New("-interrupt-after must not be negative")
	}
	if c.samplesRoot == "" {
		c.samplesRoot = filepath.Join(c.outDir, "samples")
	}
	return c, nil
}

func run(args []string, out, errw *os.File) int {
	c, err := parseFlags(args, errw)
	if err != nil {
		fmt.Fprintf(errw, "media-audit: %v\n", err)
		fmt.Fprintln(errw, "usage: media-audit -out DIR [-generate] [-chunk N] [-resume] [-interrupt-after N]")
		return exitFatal
	}
	if c.generate {
		if _, err := fixtures.Generate(c.samplesRoot); err != nil {
			fmt.Fprintf(errw, "media-audit: generating fixtures: %v\n", err)
			return exitFatal
		}
		fmt.Fprintf(out, "generated fixtures in %s\n", c.samplesRoot)
	}

	// The acceptance command runs the normal scenario. Each scenario gets its
	// own checkpoint directory so a resume can never mix inputs.
	scenario := fixtures.ScenarioNormal
	if mode := os.Getenv("MEDIA_AUDIT_SCENARIO"); mode != "" {
		scenario = mode
	}
	dir := filepath.Join(c.samplesRoot, scenario)
	manifest, err := fixtures.LoadManifest(dir)
	if err != nil {
		fmt.Fprintf(errw, "media-audit: %v (run with -generate first)\n", err)
		return exitFatal
	}
	if c.timecodeMode == "" {
		c.timecodeMode = manifest.TimecodeMode
	}
	// An unsupported mode is a parameter error, not a per-segment failure:
	// refuse it before any work is done so the exit code is 1.
	if _, ok := timecode.ModeByName(c.timecodeMode); !ok {
		fmt.Fprintf(errw, "media-audit: unsupported -timecode-mode %q (want 24, 25, 29.97ndf, 29.97df or 50)\n", c.timecodeMode)
		return exitFatal
	}

	interval, _ := model.ParseRat(c.interval)
	snap, _ := model.ParseRat(c.snapTolerance)
	in := pipeline.Inputs{
		OutDir:         filepath.Join(c.outDir, "checkpoints", scenario),
		MP4Path:        manifest.MP4,
		TimecodePath:   manifest.Timecodes,
		TimecodeMode:   c.timecodeMode,
		Width:          c.width,
		Height:         c.height,
		Interval:       interval,
		SnapTolerance:  snap,
		ChunkSize:      c.chunk,
		MaxDriftPPM:    c.maxDriftPPM,
		FallbackScan:   !c.noFallback,
		Resume:         c.resume,
		InterruptAfter: c.interruptAfter,
		VerifyOnly:     c.verify,
		RTPStreams:     manifest.RTP,
	}
	ids := make([]string, 0, len(manifest.Subtitles))
	for id := range manifest.Subtitles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		in.Subtitles = append(in.Subtitles, pipeline.SubtitleInput{TrackID: id, Path: manifest.Subtitles[id]})
	}

	if err := os.MkdirAll(in.OutDir, 0o755); err != nil {
		fmt.Fprintf(errw, "media-audit: %v\n", err)
		return exitFatal
	}
	st, perr := pipeline.Run(in)
	printRun(out, scenario, st, c.quiet)
	if perr != nil {
		var interrupted *pipeline.InterruptedError
		if errors.As(perr, &interrupted) {
			fmt.Fprintf(out, "status=interrupted after=%d committed_chunks=%d\n",
				interrupted.After, st.CommittedChunks)
			// An interruption is recoverable: progress is intact and -resume
			// finishes the job.
			return exitRecoverable
		}
		var me *model.Error
		if errors.As(perr, &me) {
			fmt.Fprintf(errw, "media-audit: %s\n", me.Error())
			if me.Code.Fatal() {
				return exitFatal
			}
			return exitRecoverable
		}
		fmt.Fprintf(errw, "media-audit: %v\n", perr)
		return exitFatal
	}
	return st.ExitCode()
}

func printRun(w *os.File, scenario string, st pipeline.Stats, quiet bool) {
	fmt.Fprintf(w, "scenario=%s\n", scenario)
	fmt.Fprintf(w, "roundtrip_consistency=%s\n", st.Metrics.RoundtripConsistency)
	fmt.Fprintf(w, "keyframe_index_hit_rate=%s\n", st.Metrics.KeyframeIndexHitRate)
	fmt.Fprintf(w, "segment_hash_match=%s\n", st.Metrics.SegmentHashMatch)
	fmt.Fprintf(w, "timeline_items=%d frames=%d frame_errors=%d\n", st.Items, st.Frames, st.FrameFailures)
	fmt.Fprintf(w, "chunks_total=%d committed_chunks=%d verified_chunks=%d replayed_chunks=%d\n",
		st.Chunks, st.CommittedChunks, st.VerifiedChunks, st.ReplayedChunks)
	fmt.Fprintf(w, "diagnostics=%d warnings=%d recoverable=%d config_digest=%s\n",
		len(st.Errors), st.Warnings, st.Recoverable, short(st.ConfigDigest))
	for _, tr := range st.RTP.Tracks {
		status := "ok"
		if tr.Failed {
			status = "failed"
		}
		fmt.Fprintf(w, "rtp_track=%s packets=%d wraps=%d drift_ppm=%s correction=%s status=%s\n",
			tr.TrackID, tr.Packets, tr.Wraps, tr.PPM, tr.Correction, status)
	}
	if quiet {
		return
	}
	for _, e := range st.Errors {
		kind := "error"
		if e.Warning {
			kind = "warning"
		}
		fmt.Fprintf(w, "diagnostic=%s code=%s subsystem=%s track=%s pos=%s message=%s\n",
			kind, e.Code, e.Subsystem, dash(e.Track), e.Pos.String(), e.Message)
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
