// Command mediasync is the terminal-only demonstration and acceptance driver
// for the mediasync recovery library. All real logic lives in the internal
// packages; this entry point only parses flags, runs the pipeline and prints
// the deterministic summary.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mediasync/internal/fanout"
	"mediasync/internal/fixture"
	"mediasync/internal/index"
	"mediasync/internal/manifest"
	"mediasync/internal/metrics"
	"mediasync/internal/repair"
)

// exitCode values follow the acceptance contract: 0 success, 2 bad arguments,
// 3 unrecoverable structural damage.
const (
	exitOK         = 0
	exitBadArgs    = 2
	exitCorrupt    = 3
)

type options struct {
	seed      int64
	duration  string
	bitrates  []int
	outDir    string
	scenario  fixture.Scenario
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	opts, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argument error: %v\n", err)
		return exitBadArgs
	}
	durationNS, derr := fixture.ParseDurationNS(opts.duration)
	if derr != nil {
		fmt.Fprintf(os.Stderr, "argument error: %v\n", derr)
		return exitBadArgs
	}
	cfg, cerr := fixture.DefaultConfig(opts.seed, durationNS, opts.bitrates)
	if cerr != nil {
		fmt.Fprintf(os.Stderr, "argument error: %v\n", cerr)
		return exitBadArgs
	}
	if opts.scenario == fixture.CaseWrap {
		cfg.ForceWrap = true
		cfg.DriftPPM = 250
	}

	stream, gerr := fixture.Generate(cfg, opts.scenario)
	if gerr != nil {
		fmt.Fprintf(os.Stderr, "generation failed: %v\n", gerr)
		return exitCorrupt
	}
	ladder, rerr := fanout.Recover(stream)
	if rerr != nil {
		fmt.Fprintf(os.Stderr, "unrecoverable stream: %v\n", rerr)
		return exitCorrupt
	}

	built, durations, ierr := buildIndexes(cfg, ladder, opts)
	if ierr != nil {
		fmt.Fprintf(os.Stderr, "indexing failed: %v\n", ierr)
		return exitCorrupt
	}
	indexes := make([]*index.MP4Index, len(built))
	for i, bi := range built {
		indexes[i] = bi.idx
	}
	mb, _ := manifest.NewBuilder(500_000_000, cfg.BaseFrameNS)
	man, merr := mb.Build(ladder, durations)
	if merr != nil {
		fmt.Fprintf(os.Stderr, "manifest failed: %v\n", merr)
		return exitCorrupt
	}
	rep, xerr := metrics.NewCollector().Collect(opts.seed, opts.scenario, stream, ladder, indexes, man)
	if xerr != nil {
		fmt.Fprintf(os.Stderr, "reporting failed: %v\n", xerr)
		return exitCorrupt
	}

	printReport(rep)
	if err := writeArtifacts(opts.outDir, built, man); err != nil {
		fmt.Fprintf(os.Stderr, "artifact write failed: %v\n", err)
		return exitCorrupt
	}
	// Corrupt runs must surface damage explicitly but still exit 0 when the
	// library isolated it safely; a fully unrecoverable stream returns 3 above.
	if opts.scenario == fixture.CaseCorrupt && rep.CorruptPackets == 0 {
		fmt.Fprintln(os.Stderr, "corrupt case produced no detectable damage")
		return exitCorrupt
	}
	return exitOK
}

type builtIndex struct {
	idx  *index.MP4Index
	data []byte
}

func buildIndexes(cfg fixture.Config, ladder *fanout.Ladder, opts options) ([]builtIndex, [][]int64, *statusErr) {
	built := make([]builtIndex, len(ladder.Streams()))
	durations := make([][]int64, len(ladder.Streams()))
	for i, res := range ladder.Streams() {
		d, derr := repair.FrameDurations(res.Samples)
		if derr != nil {
			return nil, nil, &statusErr{derr}
		}
		durations[i] = d
		idx, data, berr := index.NewBuilder(uint32(i + 1)).Build(res.Samples, d, cfg.ClockRate)
		if berr != nil {
			return nil, nil, &statusErr{berr}
		}
		built[i] = builtIndex{idx: idx, data: data}
	}
	return built, durations, nil
}

type statusErr struct{ inner error }

func (e *statusErr) Error() string { return e.inner.Error() }

func parseArgs(args []string) (options, error) {
	opts := options{scenario: fixture.CaseClean, duration: "30s", outDir: "./out"}
	if len(args) == 0 || args[0] != "demo" {
		return opts, fmt.Errorf("expected 'demo' subcommand")
	}
	for i := 1; i < len(args); i++ {
		arg := args[i]
		val := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag %s needs a value", arg)
			}
			i++
			return args[i], nil
		}
		switch arg {
		case "--seed":
			v, err := val()
			if err != nil {
				return opts, err
			}
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return opts, fmt.Errorf("invalid seed %q", v)
			}
			opts.seed = n
		case "--duration":
			v, err := val()
			if err != nil {
				return opts, err
			}
			opts.duration = v
		case "--bitrates":
			v, err := val()
			if err != nil {
				return opts, err
			}
			br, err := parseBitrates(v)
			if err != nil {
				return opts, err
			}
			opts.bitrates = br
		case "--out":
			v, err := val()
			if err != nil {
				return opts, err
			}
			opts.outDir = v
		case "--case":
			v, err := val()
			if err != nil {
				return opts, err
			}
			sc := fixture.Scenario(v)
			switch sc {
			case fixture.CaseClean, fixture.CaseLoss, fixture.CaseWrap, fixture.CaseCorrupt:
				opts.scenario = sc
			default:
				return opts, fmt.Errorf("unknown case %q (want clean|loss|wrap|corrupt)", v)
			}
		default:
			return opts, fmt.Errorf("unknown flag %q", arg)
		}
	}
	if len(opts.bitrates) == 0 {
		// Default ladder for the short-form acceptance invocation that omits
		// --bitrates (e.g. the documented corrupt-case command).
		opts.bitrates = []int{1_200_000, 2_400_000, 4_800_000}
	}
	return opts, nil
}

func parseBitrates(s string) ([]int, error) {
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid bitrate %q", p)
		}
		out = append(out, n)
	}
	return out, nil
}

func printReport(rep *metrics.Report) {
	fmt.Printf("scenario=%s seed=%d\n", rep.Scenario, rep.Seed)
	for _, r := range rep.Renditions {
		fmt.Printf("rendition=%d\n", r.Rendition)
		fmt.Printf("  packets_in=%d\n", r.PacketsIn)
		fmt.Printf("  loss_detected=%d\n", r.LossDetected)
		fmt.Printf("  loss_recovered=%d\n", r.LossRecovered)
		fmt.Printf("  drift_ppm=%.3f\n", r.DriftPPM)
		fmt.Printf("  wraps=%d\n", r.Wraps)
		fmt.Printf("  target_bps=%d\n", r.TargetBPS)
		fmt.Printf("  actual_bps=%d\n", r.ActualBPS)
		fmt.Printf("  bitrate_error_pct=%.3f\n", r.BitrateErrorPct)
		fmt.Printf("  quality_psnr_db=%.3f\n", r.QualityPSNRDB)
		fmt.Printf("  random_access_points=%d\n", r.RandomAccess)
		fmt.Printf("  index_sha256=%s\n", r.IndexSHA256)
	}
	fmt.Printf("manifest_error_ms=%.3f\n", rep.ManifestErrorMS)
	fmt.Printf("corrupt_packets=%d\n", rep.CorruptPackets)
	fmt.Printf("rejected_boundaries=%d\n", rep.RejectedBoundaries)
	fmt.Printf("index_sha256=%s\n", rep.IndexSHA256)
}

func writeArtifacts(dir string, built []builtIndex, man *manifest.Manifest) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for i, bi := range built {
		// Persist the real MP4 byte stream and a human-readable index sidecar.
		mp4Path := filepath.Join(dir, fmt.Sprintf("rendition-%d.mp4", i))
		if err := os.WriteFile(mp4Path, bi.data, 0o644); err != nil {
			return err
		}
		sidecar := filepath.Join(dir, fmt.Sprintf("rendition-%d.index.txt", i))
		if err := os.WriteFile(sidecar, []byte(renderIndexSidecar(bi.idx)), 0o644); err != nil {
			return err
		}
	}
	manifestPath := filepath.Join(dir, "manifest.txt")
	return os.WriteFile(manifestPath, []byte(renderManifestText(man)), 0o644)
}

func renderIndexSidecar(idx *index.MP4Index) string {
	var b strings.Builder
	fmt.Fprintf(&b, "track_id=%d file_size=%d mdat_offset=%d payload=%d rejected=%d rap=%d sha256=%s\n",
		idx.TrackID, idx.FileSize, idx.MDATDataOffset, idx.MDATPayloadBytes,
		idx.RejectedSamples, len(idx.RandomAccess), metricsHex(idx.SHA256))
	for _, e := range idx.Entries {
		fmt.Fprintf(&b, "sample=%d offset=%d size=%d key=%t media_ns=%d\n",
			e.SampleIndex, e.Offset, e.Size, e.IsKeyframe, e.MediaNS)
	}
	return b.String()
}

func renderManifestText(m *manifest.Manifest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "target_ns=%d frame_ns=%d max_error_ms=%.3f rejected=%d renditions=%d\n",
		m.TargetSegNS, m.FramePeriodNS, m.MaxErrorMS(), m.RejectedPoints, len(m.Renditions))
	for _, rm := range m.Renditions {
		for _, s := range rm.Segments {
			fmt.Fprintf(&b, "rend=%d segment=%d start_ns=%d duration_ns=%d switchable=%t\n",
				rm.Rendition, s.Index, s.StartNS, s.DurationNS, s.Switchable)
		}
	}
	return b.String()
}

func metricsHex(sum [32]byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 64)
	for i, v := range sum {
		out[i*2] = digits[v>>4]
		out[i*2+1] = digits[v&0x0f]
	}
	return string(out)
}
