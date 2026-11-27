package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"archivecheck/internal/asset"
	"archivecheck/internal/fault"
	"archivecheck/internal/fixture"
	"archivecheck/internal/manifest"
	"archivecheck/internal/pipeline"
	"archivecheck/internal/recovery"
	"archivecheck/internal/stream"
	"archivecheck/internal/subtitle"
	"archivecheck/internal/timebase"
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

// analyseAll streams every source through the resumable pipeline and then runs
// the structural, subtitle and timecode analyses.
func analyseAll(ctx context.Context, out io.Writer, cfg pipeline.Config, samples []*sample, stateDir string) (*analysis, error) {
	res := &analysis{mp4: map[string]*stream.Structure{}, ceiling: cfg.Bound(2)}
	byName := map[string]*sample{}
	for _, s := range samples {
		byName[s.source.Name] = s
	}
	fmt.Fprintln(out, "== pipeline and recovery =================================")

	for _, s := range samples {
		label := filepath.Base(s.source.Name)
		// The interruption drill always starts from scratch so every run
		// proves that an interrupted run resumes from a verified boundary.
		interruptDemo := label == "clip-truncated.mp4"
		// Interactive runs keep a per-source checkpoint that a restart can
		// resume from. The interruption drill always starts from scratch, so
		// every demo proves that a cancelled run resumes from a verified
		// boundary.
		cpPath := filepath.Join(stateDir, "resume-"+s.source.Short()+".json")
		if interruptDemo {
			cpPath = filepath.Join(stateDir, "interrupt-drill.json")
			if err := os.Remove(cpPath); err != nil && !os.IsNotExist(err) {
				return nil, err
			}
		}
		previous, hasPrevious, err := recovery.Load(cpPath)
		if err != nil {
			return nil, err
		}
		var cp *recovery.Checkpoint
		if hasPrevious && !interruptDemo && previous.Verify(s.source, cfg) == nil {
			cp = previous
		} else {
			cp = recovery.New(s.source, cfg)
		}
		sess, err := recovery.Open(s.source, cp, cfg, "scan", "emit")
		if err != nil {
			return nil, err
		}
		start := int64(0)
		if !interruptDemo {
			start = sess.Boundary("scan")
			if e := sess.Boundary("emit"); e < start {
				start = e
			}
			if start > s.source.Size {
				start = s.source.Size
			}
		}

		var interruptAt int64 = -1
		if interruptDemo {
			if n := int64(2 * cfg.ChunkSize); n < s.source.Size {
				interruptAt = n
			}
		}
		rep, cause := runPipeline(ctx, s.source, cfg, sess, start, interruptAt, false)
		if cause != nil && !fault.HasKind(cause, fault.KindCancelled) {
			return nil, fmt.Errorf("stage %s: %w", label, cause)
		}
		res.addReport(rep)
		if cause != nil {
			if !allStagesShare(rep, cause) {
				return nil, fmt.Errorf("stage %s: cancellation cause was not shared", label)
			}
			res.cancelShared = true
			res.cancelReason = cause.Error()
			fmt.Fprintf(out, "  %-22s interrupted at byte %d: %v\n", label, fault.OffsetOf(cause), cause)
			if _, err := sess.Checkpoint().Save(cpPath); err != nil {
				return nil, err
			}
			next, ok, err := recovery.Load(cpPath)
			if err != nil || !ok {
				return nil, fmt.Errorf("reload checkpoint: %v", err)
			}
			resumed, err := recovery.Open(s.source, next, cfg, "scan", "emit")
			if err != nil {
				return nil, fmt.Errorf("reopen checkpoint: %w", err)
			}
			boundary, err := resumed.Resume("scan", "emit")
			if err != nil {
				return nil, fmt.Errorf("stage %s: rewind to a shared boundary: %w", label, err)
			}
			if boundary <= 0 || boundary > s.source.Size {
				return nil, fmt.Errorf("stage %s: unusable resume boundary %d", label, boundary)
			}
			res.resumeOffset = boundary
			rep2, cause2 := runPipeline(ctx, s.source, cfg, resumed, boundary, -1, true)
			if cause2 != nil {
				return nil, fmt.Errorf("stage %s: resume failed: %w", label, cause2)
			}
			res.addReport(rep2)
			for _, name := range []string{"scan", "emit"} {
				resumed.Finish(name, "completed after resume")
			}
			res.resumeOK = true
			res.resumeStages = resumed.Snapshot()
			fmt.Fprintf(out, "  %-22s resumed at byte %d: %s\n", label, boundary, stageLine(rep2))
			sess = resumed
		} else {
			for _, name := range []string{"scan", "emit"} {
				sess.Finish(name, "completed")
			}
			switch {
			case start == s.source.Size:
				fmt.Fprintf(out, "  %-22s already verified to byte %d; nothing to re-read\n", label, start)
			default:
				fmt.Fprintf(out, "  %-22s resumed from byte %d: %s\n", label, start, stageLine(rep))
			}
		}
		if _, err := sess.Checkpoint().Save(cpPath); err != nil {
			return nil, err
		}
	}

	// A slow consumer proves backpressure without unbounded buffering: the
	// 20 KiB clip yields several chunks, so the producer must wait.
	slowRep, err := runSlowConsumer(ctx, byName["samples/clip-valid.mp4"].source, cfg)
	if err != nil {
		return nil, err
	}
	res.addReport(slowRep)
	fmt.Fprintf(out, "  slow-consumer          %s\n", stageLine(slowRep))
	res.peakOK = res.peak <= res.ceiling && res.waits > 0
	res.pipelineHealthy = res.peakOK && res.cancelShared && res.resumeOK
	fmt.Fprintln(out)

	if err := collectStructure(res, byName); err != nil {
		return nil, err
	}
	collectSubtitles(res, byName)
	collectTimecodes(res, byName)
	sort.SliceStable(res.damage, func(i, j int) bool {
		if res.damage[i].Source != res.damage[j].Source {
			return res.damage[i].Source < res.damage[j].Source
		}
		return res.damage[i].Start < res.damage[j].Start
	})
	return res, nil
}

func (a *analysis) addReport(rep pipeline.Report) {
	a.reports = append(a.reports, rep)
	if rep.PeakOutstanding > a.peak {
		a.peak = rep.PeakOutstanding
	}
	a.waits += rep.BackpressureWaits
}

// runPipeline streams src[start:] through the bounded two-stage pipeline,
// committing each chunk to the checkpoint. A non-negative interruptAt cancels
// the run from the scan stage once that byte is reached.
func runPipeline(ctx context.Context, src *asset.Source, cfg pipeline.Config, sess *recovery.Session, start, interruptAt int64, resumed bool) (pipeline.Report, error) {
	section := io.NewSectionReader(bytesReaderAt(src.Bytes()), start, src.Size-start)
	// Resumed runs start mid-source, so chunk offsets are rebased onto the
	// original source coordinates before they reach the checkpoint.
	rebase := func(c pipeline.Chunk) pipeline.Chunk {
		c.Offset += start
		return c
	}
	scan := pipeline.Stage{Name: "scan", Handle: func(ctx context.Context, c pipeline.Chunk) error {
		sess.MarkRunning("scan")
		return sess.CommitChunk("scan", rebase(c))
	}}
	emit := pipeline.Stage{Name: "emit", Handle: func(ctx context.Context, c pipeline.Chunk) error {
		sess.MarkRunning("emit")
		c = rebase(c)
		if err := sess.CommitChunk("emit", c); err != nil {
			return err
		}
		// The drill cancels at the consumer, after that chunk is durably
		// verified, which is what an interrupted slow consumer looks like.
		if interruptAt >= 0 && c.Offset+int64(len(c.Data)) >= interruptAt {
			return fault.New(fault.StagePipeline, fault.KindCancelled, src.Name,
				c.Offset+int64(len(c.Data)), "simulated interruption at a verified boundary")
		}
		return nil
	}}
	return pipeline.Run(ctx, section, src.Size-start, cfg, scan, emit)
}

func runSlowConsumer(ctx context.Context, src *asset.Source, cfg pipeline.Config) (pipeline.Report, error) {
	stage := pipeline.Stage{Name: "slow", Handle: func(ctx context.Context, c pipeline.Chunk) error {
		select {
		case <-time.After(300 * time.Microsecond):
		case <-ctx.Done():
			return context.Cause(ctx)
		}
		return nil
	}}
	return pipeline.Run(ctx, strings.NewReader(string(src.Bytes())), src.Size, cfg, stage)
}

func bytesReaderAt(b []byte) io.ReaderAt { return &readerAt{b: b} }

type readerAt struct{ b []byte }

func (r *readerAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(r.b)) {
		return 0, io.EOF
	}
	n := copy(p, r.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func allStagesShare(rep pipeline.Report, cause error) bool {
	for _, s := range rep.Stages {
		if s.Err == nil || s.Err.Error() != cause.Error() {
			return false
		}
	}
	return true
}

func stageLine(rep pipeline.Report) string {
	parts := make([]string, 0, len(rep.Stages))
	for _, s := range rep.Stages {
		status := "done"
		if s.Err != nil {
			status = "cancelled"
		}
		parts = append(parts, fmt.Sprintf("%s=%s chunks=%d bytes=%d waits=%d", s.Name, status, s.Chunks, s.Bytes, s.Waits))
	}
	return strings.Join(parts, " ")
}

func cueRange(tr *subtitle.Track) (manifest.TimeRange, bool) {
	if len(tr.Cues) == 0 {
		return manifest.TimeRange{}, false
	}
	r := manifest.TimeRange{Source: tr.Source, Start: tr.Cues[0].Start, End: tr.Cues[0].End}
	for _, c := range tr.Cues[1:] {
		if c.Start < r.Start {
			r.Start = c.Start
		}
		if c.End > r.End {
			r.End = c.End
		}
	}
	return r, true
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func firstSpan(spans []fault.Span) string {
	if len(spans) == 0 {
		return "none"
	}
	return fmt.Sprintf("offset=%d..%d kind=%s", spans[0].Start, spans[0].End, spans[0].Kind)
}

func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

func stableNote(stored, current string) string {
	if stored == "" {
		return "checksum=" + short(current) + " (first recorded run)"
	}
	return "previous=" + short(stored) + " current=" + short(current)
}

func ms(n timebase.Nanos) string { return fmt.Sprintf("%dms", int64(n)/1e6) }

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
