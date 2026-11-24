// Command mediascan drives the local media-scan kernel against generated
// HLS/DASH fixtures and prints reproducible, path-free acceptance output.
// It is a thin driver: all processing lives in the reusable internal
// packages.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediascan/internal/fault"
	"mediascan/internal/fixture"
	"mediascan/internal/index"
	"mediascan/internal/manifest"
	"mediascan/internal/pipeline"
	"mediascan/internal/stream"
)

// checkpointName is only a basename; the file lives under the OS temp
// directory so no host-specific path enters the reports.
const checkpointName = "mediascan-checkpoint.json"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("mediascan", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	generate := fs.Bool("generate", false, "generate and scan a local scenario")
	caseName := fs.String("case", "normal", "scenario: normal|damaged|boundary")
	formats := fs.String("format", "hls,dash", "comma list: hls,dash")
	truncateBox := fs.Int("truncate-box", 0, "bytes removed from a box tail")
	wrap := fs.Bool("timestamp-wrap", false, "force 32-bit timestamp rollover")
	vfr := fs.Bool("vfr", false, "emit variable frame rate samples")
	driftPPM := fs.Float64("drift-ppm", 0, "applied source clock drift in PPM")
	duplicate := fs.Bool("duplicate-sequence", false, "emit a duplicate sequence id")
	missing := fs.Bool("missing-segment", false, "emit a missing segment")
	resume := fs.Bool("resume", false, "re-run and verify against a checkpoint")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if !*generate {
		fmt.Fprintln(os.Stderr, "mediascan: only --generate mode is supported")
		return 1
	}

	cfg, err := configForCase(*caseName, fixture.ScenarioConfig{
		TruncateBoxBytes: *truncateBox, TimestampWrap: *wrap, VFR: *vfr,
		DriftPPM: *driftPPM, DuplicateSequence: *duplicate,
		MissingSegment: *missing,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediascan: %v\n", err)
		return 1
	}
	sc, err := fixture.BuildScenario(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediascan: scenario build failed: %v\n", err)
		return 1
	}

	wantFormats, err := parseFormats(*formats)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediascan: %v\n", err)
		return 1
	}

	worst := 0
	damagedOnce := 0
	for _, f := range wantFormats {
		code, damaged := scanOne(f, sc, cfg, *resume)
		if code > worst {
			worst = code
		}
		// Damage belongs to the shared segment set, so count it once even
		// though each format reports it independently.
		if damagedOnce == 0 {
			damagedOnce = damaged
		}
	}
	fmt.Printf("total_segments=%d damage_count=%d exit_code=%d\n",
		len(sc.Segments), damagedOnce, worst)
	return worst
}

func configForCase(name string, extra fixture.ScenarioConfig) (fixture.ScenarioConfig, error) {
	cfg := fixture.DefaultConfig()
	switch name {
	case "normal":
	case "damaged":
		if extra.TruncateBoxBytes == 0 {
			extra.TruncateBoxBytes = 17
		}
		extra.TruncateTarget = 1
		// More segments give sub-tick clock drift enough span to become
		// observable above tfdt integer quantization.
		if extra.DriftPPM != 0 && cfg.Segments < 24 {
			cfg.Segments = 24
		}
	case "boundary":
		extra.MissingSegment = true
		extra.DuplicateSequence = true
	default:
		return cfg, fmt.Errorf("unknown --case %q", name)
	}
	cfg.SamplesPerSegment = extra.SamplesPerSegment
	if cfg.SamplesPerSegment == 0 {
		cfg.SamplesPerSegment = 4
	}
	cfg.TruncateBoxBytes = extra.TruncateBoxBytes
	cfg.TruncateTarget = extra.TruncateTarget
	cfg.TimestampWrap = extra.TimestampWrap
	cfg.VFR = extra.VFR
	cfg.DriftPPM = extra.DriftPPM
	cfg.DuplicateSequence = extra.DuplicateSequence
	cfg.MissingSegment = extra.MissingSegment
	return cfg, nil
}

func parseFormats(list string) ([]manifest.Format, error) {
	var out []manifest.Format
	for _, f := range strings.Split(list, ",") {
		switch strings.TrimSpace(f) {
		case "hls":
			out = append(out, manifest.FormatHLS)
		case "dash":
			out = append(out, manifest.FormatDASH)
		case "":
		default:
			return nil, fmt.Errorf("unknown --format %q", f)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one format is required")
	}
	return out, nil
}

func scanOne(f manifest.Format, sc *fixture.Scenario, cfg fixture.ScenarioConfig, resume bool) (int, int) {
	fmt.Printf("==== format=%s drift_ppm=%.0f ====\n", f, cfg.DriftPPM)
	mani, mlog := parseManifest(f, sc)
	for _, r := range mlog.Records() {
		fmt.Printf("manifest %s\n", r.String())
	}

	inputs := make([]pipeline.SegmentInput, len(sc.Segments))
	for i, s := range sc.Segments {
		var manifestDuration float64
		if i < len(mani.Segments) {
			manifestDuration = mani.Segments[i].Duration
		}
		inputs[i] = pipeline.SegmentInput{
			Name: s.Name, MediaSeq: s.MediaSeq, Present: s.Present,
			Data: s.Data, ManifestOffset: s.ManifestOffset,
			DurationSeconds: manifestDuration,
		}
	}

	p := pipeline.New(cfg.DriftPPM)
	mergeManifestFaults(p, mlog)
	summary := p.Run(inputs)

	// Multi-point drift fit from segment boundaries (source tfdt vs
	// manifest reference), recorded with before/after residuals.
	timescale := cfg.Timescale
	if ts := firstTimescale(p); ts > 0 {
		timescale = ts
	}
	parsed := make([]*pipeline.ParsedSegment, 0, len(p.Results()))
	for _, r := range p.Results() {
		parsed = append(parsed, r.Parsed)
	}
	obs := pipeline.BuildObservations(parsed, timescale)
	fit := p.EstimateDrift(timescale, obs)
	fmt.Printf("drift_estimated_ppm=%.3f drift_applied_ppm=%.3f r_squared=%.4f trusted=%d observations=%d residual_micros=%v\n",
		fit.EstimatedPPM, fit.AppliedPPM, fit.RSquared, boolInt(fit.Trusted),
		fit.Observations, fit.ResidualMicros)

	printHashes(p)
	printFaults(p)
	printSummary(mani, summary)

	if rtpFaults := scanRTP(sc); rtpFaults > 0 {
		fmt.Printf("rtp_negative_jumps=%d\n", rtpFaults)
	}

	if resume {
		if code := runResume(p, inputs, cfg); code != 0 {
			return code, summary.DamagedSegments
		}
	}
	return summary.ExitCode, summary.DamagedSegments
}

func parseManifest(f manifest.Format, sc *fixture.Scenario) (*manifest.Manifest, *fault.Log) {
	if f == manifest.FormatHLS {
		return manifest.ParseHLS(sc.HLS)
	}
	return manifest.ParseDASH(sc.DASH)
}

// mergeManifestFaults surfaces playlist-level boundary faults into the
// pipeline log so duplicate/gap findings drive the exit code.
func mergeManifestFaults(p *pipeline.Pipeline, log *fault.Log) {
	for _, r := range log.Records() {
		if r.Severity == fault.SeverityError {
			p.Log.Add(r)
		}
	}
}

func printHashes(p *pipeline.Pipeline) {
	for _, h := range p.Hasher.Segments() {
		tag := "ok"
		if h.Damaged {
			tag = "damaged"
		}
		fmt.Printf("segment=%s seq=%d status=%s input_sha256=%s output_sha256=%s\n",
			h.Segment, h.MediaSeq, tag, h.InputHash, orDash(h.OutputHash))
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func printFaults(p *pipeline.Pipeline) {
	for _, r := range p.Log.Records() {
		fmt.Printf("fault %s\n", r.String())
	}
}

func printSummary(mani *manifest.Manifest, s *pipeline.Summary) {
	fmt.Printf("manifest_segments=%d time_range=[%.3f,%.3f]\n",
		len(mani.Segments), maniStart(mani), maniEnd(mani))
	fmt.Printf("roundtrip_consistency=%.3f keyframe_hit_rate=%.3f damage_count=%d\n",
		s.RoundtripConsistency, s.KeyframeHitRate, s.DamagedSegments)
	for i, off := range s.DamageOffsets {
		fmt.Printf("damage_offset=%d isolated_range=[%d,%d] verified_prefix_bytes=%d\n",
			off, s.IsolatedRanges[i][0], s.IsolatedRanges[i][1], s.VerifiedPrefixBytes)
	}
	fmt.Printf("processable_after_damage=%d fingerprint=%s\n",
		s.ProcessableAfter, s.Fingerprint)
}

func maniStart(m *manifest.Manifest) float64 { a, _ := m.TimeRange(); return a }
func maniEnd(m *manifest.Manifest) float64   { _, b := m.TimeRange(); return b }

// firstTimescale recovers the parsed track time base from any clean
// result, falling back to zero when every segment is damaged.
func firstTimescale(p *pipeline.Pipeline) uint32 {
	for _, r := range p.Results() {
		if r.Parsed != nil && r.Parsed.MP4 != nil && len(r.Parsed.MP4.Tracks) > 0 {
			if ts := r.Parsed.MP4.Tracks[0].Timescale; ts > 0 {
				return ts
			}
		}
	}
	return 0
}

func scanRTP(sc *fixture.Scenario) int {
	if len(sc.RTPPackets) == 0 {
		return 0
	}
	res, err := stream.ParseRTP(sc.RTPPackets, sc.RTPRate)
	if err != nil {
		return 1
	}
	fmt.Printf("rtp_packets=%d wrap_events=%d mean_frame_rate=%.3f\n",
		len(res.Packets), len(res.Wraps), res.MeanFrameRate)
	return len(res.NegativeJumps)
}

func runResume(first *pipeline.Pipeline, inputs []pipeline.SegmentInput, cfg fixture.ScenarioConfig) int {
	path := filepath.Join(os.TempDir(), checkpointName)
	if err := first.SaveCheckpoint(path); err != nil {
		fmt.Fprintf(os.Stderr, "mediascan: checkpoint write failed: %v\n", err)
		return 1
	}
	cp, err := pipeline.LoadCheckpoint(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediascan: checkpoint read failed: %v\n", err)
		return 1
	}
	resumed := pipeline.New(cfg.DriftPPM)
	resumed.Run(inputs)
	indexMatch := sameIndex(cp.Entries, resumed.Index.Snapshot())
	hashMatch := len(cp.Results) == len(resumed.Hasher.Segments())
	if hashMatch {
		for i, saved := range cp.Results {
			got := resumed.Hasher.Segments()[i]
			if saved.InputHash != got.InputHash || saved.OutputHash != got.OutputHash {
				hashMatch = false
			}
		}
	}
	fmt.Printf("resume_index_match=%d resume_hash_match=%d checkpoint=%s\n",
		boolInt(indexMatch), boolInt(hashMatch), checkpointName)
	if err := pipeline.VerifyCheckpoint(cp, resumed); err != nil {
		fmt.Printf("resume_verification=failed detail=%q\n", err.Error())
		return 2
	}
	fmt.Println("resume_verification=ok")
	return 0
}

func sameIndex(a, b []index.Entry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
