// Command mediarun is the acceptance entry point for the media pipeline.
//
// Typical runs:
//
//	mediarun --generate --out ./tmp/run --resume --verify
//	mediarun --generate --out ./tmp/bad --inject truncated,checksum --verify
//
// Exit codes: 0 success, 2 input damaged but a verifiable output was produced,
// 1 configuration or internal error.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"mediarun/internal/fault"
	"mediarun/internal/gen"
	"mediarun/internal/pipeline"
	"mediarun/internal/provenance"
)

// Exit codes are part of the acceptance contract.
const (
	exitOK       = 0
	exitInternal = 1
	exitDamaged  = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type options struct {
	generate  bool
	out       string
	inject    string
	resume    bool
	verify    bool
	segments  int
	frames    int
	keyEvery  int
	audioLate int64
	verbose   bool
}

func parseFlags(args []string) (*options, error) {
	o := &options{}
	fs := flag.NewFlagSet("mediarun", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.BoolVar(&o.generate, "generate", false, "synthesize fixtures under --out before running")
	fs.StringVar(&o.out, "out", "", "run directory (inputs and outputs)")
	fs.StringVar(&o.inject, "inject", "", "comma separated fault classes: "+strings.Join(gen.KnownInjections, ","))
	fs.BoolVar(&o.resume, "resume", false, "resume from the last complete checkpoint stage")
	fs.BoolVar(&o.verify, "verify", false, "verify segment hashes, keyframe index and roundtrip")
	fs.IntVar(&o.segments, "segments", 4, "number of output segments")
	fs.IntVar(&o.frames, "frames-per-segment", 8, "video frames per segment")
	fs.IntVar(&o.keyEvery, "keyframe-every", 10, "keyframe interval in frames")
	fs.Int64Var(&o.audioLate, "audio-late-ms", 0, "delay audio start by this many milliseconds")
	fs.BoolVar(&o.verbose, "v", false, "print per-fault detail")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if o.out == "" {
		// The documented invocation uses --out; a bare default keeps ad-hoc
		// runs working without silently writing into the repository root.
		o.out = "./tmp/mediarun"
	}
	return o, nil
}

func run(args []string, stdout, stderr *os.File) int {
	opts, err := parseFlags(args)
	if err != nil {
		fmt.Fprintf(stderr, "config_error: %v\n", err)
		return exitInternal
	}

	inject := splitList(opts.inject)
	if err := gen.ValidateInjections(inject); err != nil {
		fmt.Fprintf(stderr, "config_error: %v\n", err)
		return exitInternal
	}
	cfg := pipeline.DefaultConfig()
	cfg.SegmentCount = opts.segments
	// `--inject desync` implies a deliberate 100 ms audio lead so the fault
	// class stands alone; an explicit --audio-late-ms overrides it.
	lateMs := opts.audioLate
	if lateMs == 0 && contains(inject, "desync") {
		lateMs = 100
	}
	if lateMs != 0 {
		cfg.SyncOffsetTicks = lateMs * int64(cfg.Timescale) / 1000
	}
	fixture := gen.Options{
		Dir:           opts.out,
		SegmentCount:  opts.segments,
		FramesPerSeg:  opts.frames,
		KeyframeEvery: opts.keyEvery,
		AudioOffsetNs: 0,
		Inject:        inject,
	}
	if opts.generate {
		res, err := gen.Generate(fixture)
		if err != nil {
			fmt.Fprintf(stderr, "generate_error: %v\n", err)
			return exitInternal
		}
		fmt.Fprintf(stdout, "generated frames=%d packets=%d inject=%s\n",
			res.Frames, res.Packets, strings.Join(res.Injected, ","))
	}

	runner, err := pipeline.New(opts.out, pipeline.Inputs{
		FramesPath: fixturePath(opts.out, "frames.mfr"),
		AudioPath:  fixturePath(opts.out, "audio.rtp"),
	}, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "config_error: %v\n", err)
		return exitInternal
	}
	if err := runner.Resume(opts.resume); err != nil {
		fmt.Fprintf(stderr, "resume_error: %v\n", err)
		return exitInternal
	}
	m, err := runner.Run()
	if err != nil {
		// A fault raised by a stage is an internal failure of this run, but it
		// is still printed with its stage, kind and offset so it can be traced
		// to the source bytes.
		fmt.Fprintf(stderr, "run_error: %v\n", err)
		if fe, ok := fault.As(err); ok {
			fmt.Fprintf(stderr, "run_fault stage=%s kind=%s offset=%d track=%d\n",
				fe.Stage, fe.Kind, fe.Offset, fe.Track)
		}
		return exitInternal
	}

	if opts.verify {
		vr, err := pipeline.Verify(opts.out, m)
		if err != nil {
			fmt.Fprintf(stderr, "verify_error: %v\n", err)
			return exitInternal
		}
		pipeline.ApplyBaseline(vr, runner.PreviousManifest())
		report(stdout, opts, m, vr, runner)
	} else {
		report(stdout, opts, m, nil, runner)
	}
	if hasUnrepairedDamage(m) {
		return exitDamaged
	}
	return exitOK
}

func report(stdout *os.File, opts *options, m *provenance.Manifest, vr *pipeline.VerifyReport, runner *pipeline.Runner) {
	fmt.Fprintf(stdout, "frames=%d audio_packets=%d segments=%d\n",
		m.Metrics.Frames, m.Metrics.AudioPackets, m.Metrics.Segments)
	fmt.Fprintf(stdout, "parse_errors=%d first_error_offset=%d recovered_audio=%d recovered_video=%d dropped_audio=%d\n",
		m.Metrics.ParseErrors, firstOffset(m), m.Metrics.RecoveredAudio, m.Metrics.RecoveredVideo, m.Metrics.DroppedAudio)
	if m.Metrics.IncompleteSegments > 0 || m.Metrics.UnsyncedSegments > 0 {
		fmt.Fprintf(stdout, "incomplete_segments=%d unsynced_segments=%d\n",
			m.Metrics.IncompleteSegments, m.Metrics.UnsyncedSegments)
	}
	if opts.resume && runner.ResumedStage() != "" {
		fmt.Fprintf(stdout, "resume_stage=%s\n", resumeLabel(runner.ResumedStage()))
	}
	fmt.Fprintf(stdout, "fault_summary=%s\n", faultSummary(m))
	if vr != nil {
		fmt.Fprintf(stdout, "roundtrip_match=%d%%\n", vr.RoundtripRatio())
		fmt.Fprintf(stdout, "keyframe_hit=%d%%\n", vr.KeyframeRatio())
		fmt.Fprintf(stdout, "segment_sha256_count=%d\n", len(m.Segments))
		fmt.Fprintf(stdout, "segment_sha256_equal=%d\n", boolInt(vr.SegmentsEqual))
		fmt.Fprintf(stdout, "idempotent=%d\n", boolInt(vr.Idempotent))
		if len(vr.Misses) > 0 && opts.verbose {
			for _, miss := range vr.Misses {
				fmt.Fprintf(stdout, "verify_miss: %s\n", miss)
			}
		}
	}
	// Error categories, original offsets and recovery counts are always
	// printed: they are the diagnostic the damaged-input invocation exists to
	// produce. --verbose additionally prints per-record detail strings.
	for _, f := range m.Faults {
		if opts.verbose {
			fmt.Fprintf(stdout, "fault stage=%s kind=%s offset=%d record=%d detail=%q\n",
				f.Stage, f.Kind, f.Offset, f.Record, f.Detail)
			continue
		}
		fmt.Fprintf(stdout, "fault stage=%s kind=%s offset=%d\n", f.Stage, f.Kind, f.Offset)
	}
}

// hasUnrepairedDamage decides the damaged exit code. Damaged input that still
// produced verifiable output exits 2: parse/checksum failures, fragments left
// incomplete by unrecoverable loss, and segments that failed the sync check.
// A fully repaired run (small gaps concealed, duplicates discarded) exits 0.
func hasUnrepairedDamage(m *provenance.Manifest) bool {
	if m.Metrics.ParseErrors > 0 || m.Metrics.IncompleteSegments > 0 || m.Metrics.UnsyncedSegments > 0 {
		return true
	}
	for _, f := range m.Faults {
		if !f.Recoverable {
			return true
		}
	}
	return false
}

func resumeLabel(stage string) string {
	if stage == "" {
		return "fresh"
	}
	return stage
}

func firstOffset(m *provenance.Manifest) int64 {
	best := int64(-1)
	for _, f := range m.Faults {
		if f.Offset >= 0 && (best < 0 || f.Offset < best) {
			best = f.Offset
		}
	}
	return best
}

func faultSummary(m *provenance.Manifest) string {
	counts := map[string]int{}
	for _, f := range m.Faults {
		counts[f.Kind]++
	}
	parts := make([]string, 0, len(counts))
	for _, k := range sortedKeys(counts) {
		parts = append(parts, fmt.Sprintf("%s:%d", k, counts[k]))
	}
	return strings.Join(parts, ",")
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func fixturePath(dir, name string) string {
	return strings.TrimRight(dir, "/") + "/" + name
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
