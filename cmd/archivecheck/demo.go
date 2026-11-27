package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"archivecheck/internal/asset"
	"archivecheck/internal/fault"
	"archivecheck/internal/fixture"
	"archivecheck/internal/manifest"
	"archivecheck/internal/pipeline"
	"archivecheck/internal/recovery"
	"archivecheck/internal/stream"
	"archivecheck/internal/subtitle"
	"archivecheck/internal/timecode"
)

// demoOptions are the tunables exposed on the command line.
type demoOptions struct {
	Out   string
	Chunk int
	Queue int
	Limit int64
}

// sample is one generated input plus its loaded digest-identified source.
type sample struct {
	path   string
	source *asset.Source
}

// analysis carries every finding the manifest needs.
type analysis struct {
	mp4    map[string]*stream.Structure
	rtp    *stream.RTPStream
	tracks []manifest.TrackRef
	damage []manifest.Damage
	ranges []manifest.TimeRange
	srt    *subtitle.Track
	vtt    *subtitle.Track
	codes  []timecode.Value
	bad    []error

	reports         []pipeline.Report
	peak            int
	ceiling         int
	waits           int
	peakOK          bool
	cancelShared    bool
	cancelReason    string
	resumeOK        bool
	resumeOffset    int64
	resumeStages    []recovery.StageState
	pipelineHealthy bool
}

// runDemo generates deterministic samples, analyses them through the bounded
// pipeline, writes a verified manifest and asserts the archive invariants.
func runDemo(ctx context.Context, out io.Writer, opts demoOptions) error {
	cfg := pipeline.Config{ChunkSize: opts.Chunk, QueueSize: opts.Queue}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if opts.Limit < 1 {
		return fault.New(fault.StageDemo, fault.KindInvalid, opts.Out, 0, "limit must be positive")
	}
	samplesDir := filepath.Join(opts.Out, "samples")
	stateDir := filepath.Join(opts.Out, "state")
	for _, dir := range []string{samplesDir, stateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	fmt.Fprintf(out, "archivecheck demo\nout=%s chunk=%d queue=%d limit=%d\n\n",
		absPath(opts.Out), cfg.ChunkSize, cfg.QueueSize, opts.Limit)

	samples, err := generateSamples(samplesDir)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "== sources ===============================================")
	for _, s := range samples {
		src, err := asset.ReadAll(opts.Out, s.path, opts.Limit)
		if err != nil {
			return err
		}
		s.source = src
		fmt.Fprintf(out, "  %-22s %8d B  sha256=%s\n", src.Name, src.Size, src.Short())
	}
	fmt.Fprintln(out)

	res, err := analyseAll(ctx, out, cfg, samples, stateDir)
	if err != nil {
		return err
	}
	printAnalysis(out, res)
	printPipeline(out, res)
	if !res.pipelineHealthy {
		return fmt.Errorf("bounded pipeline expectations were not met")
	}

	m, repeat, err := buildArchive(out, opts.Out, samples, res, cfg)
	if err != nil {
		return err
	}
	verified, err := verifyManifest(opts.Out)
	if err != nil {
		return fmt.Errorf("manifest verification: %w", err)
	}
	stored, err := loadStoredChecksum(stateDir)
	if err != nil {
		return err
	}
	checksum, err := m.Checksum()
	if err != nil {
		return err
	}

	fmt.Fprintln(out, "== assertions ============================================")
	valid := res.mp4["clip-valid.mp4"]
	trunc := res.mp4["clip-truncated.mp4"]
	checks := []struct {
		name string
		ok   bool
		note string
	}{
		{"valid MP4 reports zero damaged segments", !valid.Bad(),
			fmt.Sprintf("damage=%d tracks=%d", len(valid.Damaged), len(valid.Tracks))},
		{"truncated MP4 locates its first damaged offset", trunc.Bad(), firstSpan(trunc.Damaged)},
		{"RTP capture reports every sequence gap", len(res.rtp.Gaps) >= 2,
			fmt.Sprintf("gaps=%d lost=%d", len(res.rtp.Gaps), res.rtp.Lost)},
		{"damaged samples locate at least two segments", len(res.damage) >= 2,
			fmt.Sprintf("segments=%d", len(res.damage))},
		{"subtitle scan recovers cues after damage", len(res.srt.Cues) >= 1 && len(res.srt.Problems) >= 2,
			fmt.Sprintf("cues=%d problems=%d", len(res.srt.Cues), len(res.srt.Problems))},
		{"invalid timecodes are rejected with offsets", len(res.bad) == len(fixture.TimecodesInvalid()),
			fmt.Sprintf("rejected=%d", len(res.bad))},
		{"bounded pipeline stays under its ceiling", res.peakOK,
			fmt.Sprintf("peak=%d ceiling=%d waits=%d", res.peak, res.ceiling, res.waits)},
		{"interrupted run shares one cancellation cause", res.cancelShared,
			fmt.Sprintf("cause=%.60s", res.cancelReason)},
		{"resume continues from a verified boundary", res.resumeOK,
			fmt.Sprintf("resumedAt=%d bytes", res.resumeOffset)},
		{"manifest verifies independently", verified != nil, "checksum=" + checksum},
		{"repeat run reuses every derivative", repeat.Added == 0 && repeat.Reused+repeat.Changed == len(m.Derivatives),
			fmt.Sprintf("added=%d reused=%d changed=%d", repeat.Added, repeat.Reused, repeat.Changed)},
		{"manifest checksum is stable across runs", stored == "" || stored == checksum,
			stableNote(stored, checksum)},
	}
	failed := 0
	for _, c := range checks {
		status := "PASS"
		if !c.ok {
			status = "FAIL"
			failed++
		}
		fmt.Fprintf(out, "  [%s] %-46s %s\n", status, c.name, c.note)
	}
	fmt.Fprintln(out)
	if failed > 0 {
		return fmt.Errorf("%d of %d assertions failed", failed, len(checks))
	}
	if err := saveStoredChecksum(stateDir, checksum); err != nil {
		return err
	}
	fmt.Fprintln(out, "result: OK - archive verified")
	return nil
}

// generateSamples writes every deterministic sample into dir.
func generateSamples(dir string) ([]*sample, error) {
	inputs := []struct {
		name string
		data []byte
	}{
		{"clip-valid.mp4", fixture.MP4(false)},
		{"clip-truncated.mp4", fixture.MP4(true)},
		{"capture-loss.rtp", fixture.RTP()},
		{"subs-recoverable.srt", fixture.SRTRecoverable()},
		{"subs-valid.vtt", fixture.VTTValid()},
		{"timecodes.txt", codeLines()},
	}
	var out []*sample
	for _, in := range inputs {
		path := filepath.Join(dir, in.name)
		if _, err := asset.WriteAtomic(path, in.data); err != nil {
			return nil, fmt.Errorf("write sample %s: %w", in.name, err)
		}
		out = append(out, &sample{path: "samples/" + in.name})
	}
	return out, nil
}

func codeLines() []byte {
	valid := fixture.TimecodesValid()
	invalid := fixture.TimecodesInvalid()
	var sb strings.Builder
	sb.WriteString("# label|rate|timecode\n")
	for _, label := range sortedKeys(valid) {
		fmt.Fprintf(&sb, "%s|%s|%s\n", label, rateForLabel(label), valid[label])
	}
	for _, label := range sortedKeys(invalid) {
		fmt.Fprintf(&sb, "%s|25|%s\n", label, invalid[label])
	}
	return []byte(sb.String())
}

func rateForLabel(label string) string {
	switch label {
	case "24p":
		return "24"
	case "50p":
		return "50"
	case "29.97-df", "29.97-ndf":
		return "29.97"
	default:
		return "25"
	}
}
