package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"mediamux/internal/fixture"
	"mediamux/manifest"
	"mediamux/media"
	"mediamux/pipeline"
)

// paths runs every demo scenario from one temporary root. Each scenario gets
// its own cache directory and checkpoint file so runs cannot contaminate one
// another except where the scenario intends it.
type paths struct {
	root string
	f    *demoFlags
}

func newPaths(root string, f *demoFlags) *paths { return &paths{root: root, f: f} }

func (p *paths) opts(name string) pipeline.Options {
	dir := filepath.Join(p.root, name)
	return pipeline.Options{
		MemoryLimit:    p.f.memoryLimit,
		SlowConsumerMS: p.f.slowConsumer,
		QueueDepth:     p.f.queueDepth,
		Window:         p.f.window,
		Dir:            dir,
		CheckpointPath: filepath.Join(dir, "checkpoint.json"),
	}
}

func demoNodes(n int) []string {
	out := make([]string, n)
	for i := range out {
		if i%2 == 0 {
			out[i] = "edge-a"
		} else {
			out[i] = "edge-b"
		}
	}
	return out
}

func buildInput(doc *manifest.Document, raw string, segments [][]byte, nodes []string, rtp [][][]byte) *pipeline.Input {
	return &pipeline.Input{Manifest: doc, ManifestRaw: raw, Segments: segments, Nodes: nodes, RTP: rtp}
}

// execute builds a runner and runs one input.
func execute(ctx context.Context, opts pipeline.Options, in *pipeline.Input) (*pipeline.Result, error) {
	r, err := pipeline.NewRunner(opts)
	if err != nil {
		return nil, err
	}
	return r.Run(ctx, in)
}

func (p *paths) runCleanWith(ctx context.Context, opts pipeline.Options, demo *fixture.Demo) (*pipeline.Result, error) {
	in := buildInput(demo.HLSDoc, demo.HLSRaw, demo.Segments, demoNodes(len(demo.Segments)), nil)
	return execute(ctx, opts, in)
}

func (p *paths) runClean(ctx context.Context, demo *fixture.Demo) (*pipeline.Result, error) {
	return p.runCleanWith(ctx, p.opts("clean"), demo)
}

// runDamaged replaces the first segment with a truncated fragment and the
// second with one carrying an impossible box length.
func (p *paths) runDamaged(ctx context.Context, demo *fixture.Demo) (*pipeline.Result, error) {
	segments := make([][]byte, len(demo.Segments))
	for i := range demo.Segments {
		switch i {
		case 0:
			segments[i] = demo.Truncated[i]
		case 1:
			segments[i] = demo.BadBoxLength[i]
		default:
			segments[i] = demo.Segments[i]
		}
	}
	in := buildInput(demo.HLSDoc, demo.HLSRaw, segments, demoNodes(len(segments)), nil)
	return execute(ctx, p.opts("damaged"), in)
}

// runTransport runs the duplicate (drops=false) or lossy (drops=true) RTP set.
func (p *paths) runTransport(ctx context.Context, demo *fixture.Demo, drops bool) (*pipeline.Result, error) {
	rtp := demo.DuplicateRTP
	name := "rtp-dup"
	if drops {
		rtp = demo.LostRTP
		name = "rtp-loss"
	}
	in := buildInput(demo.HLSDoc, demo.HLSRaw, demo.Segments, demoNodes(len(demo.Segments)), rtp)
	return execute(ctx, p.opts(name), in)
}

// DriftResult couples a pipeline run with its manifest-versus-media report.
type DriftResult struct {
	Result *pipeline.Result
	Report *pipeline.AlignmentReport
}

// runDrift runs the inconsistent-manifest sample and compares each declared
// duration with what the fragments actually carry.
func (p *paths) runDrift(ctx context.Context, demo *fixture.Demo) (*DriftResult, error) {
	doc, err := manifest.ParseHLS(demo.BadManifestRaw)
	if err != nil {
		return nil, fmt.Errorf("parse drift manifest: %w", err)
	}
	in := buildInput(doc, demo.BadManifestRaw, demo.Segments, demoNodes(len(demo.Segments)), nil)
	res, err := execute(ctx, p.opts("drift"), in)
	if err != nil {
		return nil, err
	}
	durations := pipeline.MediaDurationsMS(res.Segments, len(doc.Segments))
	report := pipeline.CompareManifestToMedia(pipeline.RefOf(doc), durations, 2)
	return &DriftResult{Result: res, Report: report}, nil
}

// runResume performs an interrupted run, a resumed run and a cold full run.
func (p *paths) runResume(ctx context.Context, demo *fixture.Demo) (*pipeline.Result, *pipeline.Result, error) {
	shared := p.opts("resume")
	partial := shared
	partial.ProcessSegments = 2
	if _, err := p.runCleanWith(ctx, partial, demo); err != nil {
		return nil, nil, err
	}
	resumed, err := p.runCleanWith(ctx, shared, demo)
	if err != nil {
		return nil, nil, err
	}
	cold := p.opts("resume-cold")
	fresh, err := p.runCleanWith(ctx, cold, demo)
	if err != nil {
		return nil, nil, err
	}
	return resumed, fresh, nil
}

// runManifestChange proves a manifest fault propagates into recovery: a
// partial run's checkpoint is offered a mutated playlist, which must be
// refused and reset rather than silently resumed.
func (p *paths) runManifestChange(ctx context.Context, demo *fixture.Demo) (*pipeline.Result, error) {
	shared := p.opts("manifest-change")
	partial := shared
	partial.ProcessSegments = 2
	if _, err := p.runCleanWith(ctx, partial, demo); err != nil {
		return nil, err
	}
	mutatedRaw := demo.HLSRaw + "#EXT-X-BITRATE:9000\n"
	mutatedDoc, err := manifest.ParseHLS(mutatedRaw)
	if err != nil {
		return nil, fmt.Errorf("parse mutated manifest: %w", err)
	}
	in := buildInput(mutatedDoc, mutatedRaw, demo.Segments, demoNodes(len(demo.Segments)), nil)
	return execute(ctx, shared, in)
}

// runForged seeds the cache with genuine entries, then replays with an
// expectation that every entry came from a different node.
func (p *paths) runForged(ctx context.Context, demo *fixture.Demo) (*pipeline.Result, error) {
	shared := p.opts("forged")
	seed := shared
	seed.CheckpointPath = filepath.Join(shared.Dir, "seed-checkpoint.json")
	if _, err := p.runCleanWith(ctx, seed, demo); err != nil {
		return nil, err
	}
	forged := shared
	forged.CheckpointPath = filepath.Join(shared.Dir, "verify-checkpoint.json")
	forged.FakeSourceNode = "edge-forged"
	return p.runCleanWith(ctx, forged, demo)
}

// runReplay replays the clean corpus against a fresh checkpoint over the same
// cache, so every segment must prove a verified cache hit.
func (p *paths) runReplay(ctx context.Context, demo *fixture.Demo) (*pipeline.Result, error) {
	shared := p.opts("clean")
	replay := shared
	replay.CheckpointPath = filepath.Join(shared.Dir, "replay-checkpoint.json")
	return p.runCleanWith(ctx, replay, demo)
}

// runSlow stalls the consumer past the producer's budget. The branch must stop
// with the checkpoint resting on the last fully delivered segment.
func (p *paths) runSlow(ctx context.Context, demo *fixture.Demo, stallMS int64) (*pipeline.Result, error) {
	slow := p.opts("slow")
	slow.StallBeforeFirst = true
	slow.StallMS = stallMS
	if slow.StallMS == 0 {
		slow.StallMS = 2 * slow.SlowConsumerMS
	}
	if slow.StallMS <= 0 {
		slow.StallMS = 1
	}
	return p.runCleanWith(ctx, slow, demo)
}

// emitRun prints one path's diagnostics with a stable prefix.
func emitRun(out *strings.Builder, name string, res *pipeline.Result) {
	fmt.Fprintf(out, "path.%s.segments=%d\n", name, len(res.Segments))
	fmt.Fprintf(out, "path.%s.corrupt_segments=%d\n", name, res.CorruptSegments)
	fmt.Fprintf(out, "path.%s.actual_bps=%d\n", name, res.Quality.ActualBPS)
	fmt.Fprintf(out, "path.%s.peak_buffer_bytes=%d\n", name, res.PeakBufferBytes)
	fmt.Fprintf(out, "path.%s.result_sha256=%s\n", name, res.ResultSHA256)
	for _, e := range res.Events {
		fmt.Fprintf(out, "path.%s.event=%s segment=%d %s\n", name, e.Kind, e.Segment, e.Detail)
	}
	if res.CorruptSegments > 0 {
		fmt.Fprintf(out, "path.%s.first_damage_segment=%d\n", name, firstDamage(res))
	}
}

func firstDamage(res *pipeline.Result) int {
	for _, s := range res.Segments {
		if s.Corrupt {
			return s.Index
		}
	}
	return -1
}

func dupExcludedBytes(res *pipeline.Result) int64 {
	var bytes int64
	for _, s := range res.Segments {
		if s.RTP != nil {
			bytes += s.RTP.DuplicateBytes
		}
	}
	return bytes
}

// transportOnly counts segments flagged corrupt solely by transport evidence,
// proving gaps are distinguished from container damage.
func transportOnly(res *pipeline.Result) int {
	n := 0
	for _, s := range res.Segments {
		if s.Corrupt && s.RTP != nil && s.RTP.Corrupt && len(s.Damaged) == 0 {
			n++
		}
	}
	return n
}

func countGaps(res *pipeline.Result) int {
	n := 0
	for _, s := range res.Segments {
		if s.RTP != nil {
			n += len(s.RTP.Gaps)
		}
	}
	return n
}

func replayAllFromCache(res *pipeline.Result) bool {
	if len(res.Segments) == 0 {
		return false
	}
	for _, s := range res.Segments {
		if !s.FromCache {
			return false
		}
	}
	return true
}

// countEvents counts diagnostics of one kind.
func countEvents(res *pipeline.Result, kind string) int {
	n := 0
	for _, e := range res.Events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func maxOf(vals ...int64) int64 {
	var m int64
	for _, v := range vals {
		if v > m {
			m = v
		}
	}
	return m
}

// classify maps a pipeline error to an exit code.
func classify(err error) int {
	if err == nil {
		return exitOK
	}
	if media.HasCode(err, media.CodeInput) {
		return exitInput
	}
	var se *media.Error
	if errors.As(err, &se) {
		switch se.Phase {
		case media.PhaseCheckpoint, media.PhaseCache:
			return exitRecovery
		}
	}
	return exitRecovery
}

// checkInvariants asserts the acceptance conditions on the produced evidence.
func checkInvariants(f *demoFlags, clean, damaged *pipeline.Result, drift *DriftResult,
	resume, fresh, forged, changed, slow *pipeline.Result, peak int64) error {
	if clean.CorruptSegments != 0 {
		return fmt.Errorf("clean path reported %d corrupt segments", clean.CorruptSegments)
	}
	if clean.Quality.BitrateDeviationPct < 0 || clean.Quality.BitrateDeviationPct > 5 {
		return fmt.Errorf("bitrate deviation %.2f%% outside 0-5%%", clean.Quality.BitrateDeviationPct)
	}
	if clean.Quality.AlignmentErrorMS > 2 {
		return fmt.Errorf("timeline alignment error %dms exceeds 2ms", clean.Quality.AlignmentErrorMS)
	}
	if damaged.CorruptSegments < 1 {
		return errors.New("damaged path did not locate any corrupt segment")
	}
	if len(drift.Report.Mismatches) < 1 {
		return errors.New("manifest drift was not detected")
	}
	if forged.Cache.SourceRejects < 1 {
		return errors.New("forged cache source was accepted")
	}
	if countEvents(changed, "checkpoint_reset") < 1 {
		return errors.New("changed manifest was not refused by the checkpoint")
	}
	if changed.Checkpoint == nil || changed.Checkpoint.Cursor != len(clean.Segments) {
		return errors.New("manifest-change recovery did not complete a full rerun")
	}
	if resume.ResultSHA256 != fresh.ResultSHA256 {
		return errors.New("resumed run digest differs from a cold full run")
	}
	if resume.Checkpoint == nil || resume.Checkpoint.ResultDigest != resume.ResultSHA256 {
		return errors.New("resume checkpoint digest is inconsistent")
	}
	if peak > f.memoryLimit {
		return fmt.Errorf("peak buffer %d exceeds memory limit %d", peak, f.memoryLimit)
	}
	// A zero budget disables the slow-consumer verdict by definition, so the
	// invariant only applies when the operator asked for one.
	if f.slowConsumer > 0 {
		if !slow.SlowConsumer {
			return errors.New("slow-consumer path did not stop")
		}
		if slow.Checkpoint == nil || slow.Checkpoint.Cursor != slow.Checkpoint.LastCompleteIdx+1 {
			return errors.New("slow-consumer checkpoint is not at a complete boundary")
		}
		if slow.Checkpoint.Cursor >= len(clean.Segments) {
			return errors.New("slow-consumer path processed every segment despite the stall")
		}
	}
	return nil
}
