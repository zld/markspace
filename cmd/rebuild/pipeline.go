package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rtp-rebuild/internal/align"
	"rtp-rebuild/internal/clock"
	"rtp-rebuild/internal/events"
	"rtp-rebuild/internal/fault"
	"rtp-rebuild/internal/ingest"
	"rtp-rebuild/internal/manifest"
	"rtp-rebuild/internal/mp4"
	"rtp-rebuild/internal/rtcp"
	"rtp-rebuild/internal/rtp"
	"rtp-rebuild/internal/sample"
	"rtp-rebuild/internal/stage"
	"rtp-rebuild/internal/validate"
)

// reorderWindow is the extended sequence window used for reordering.
const reorderWindow = rtp.DefaultWindow

// app holds one invocation's state.
type app struct {
	cfg      *Config
	ws       *workspace
	mf       *manifest.Manifest
	work     []*work
	start    time.Time
	peakByte int64
	peakPkts int64
	softErrs []error
	report   *report
	// repeatedManifestMismatch records that a previous run's manifest differed
	// from the one this run would publish for the same inputs.
	repeatedManifestMismatch bool
	// unpublished records that validation rejected the artifacts and staged
	// copies were discarded.
	unpublished bool
	// previousManifest is the digest of the manifest found at start-up, before
	// this run writes anything.
	previousManifest string
}

// execute runs the pipeline and returns the report. A nil error means every
// stage completed (possibly with recoverable damage recorded in the report).
func execute(ctx context.Context, cfg *Config) (*report, error) {
	start := time.Now()
	ws, err := newWorkspace(cfg.OutDir)
	if err != nil {
		return nil, err
	}
	a := &app{cfg: cfg, ws: ws, start: start, previousManifest: completedManifestAt(ws)}

	if err := a.prepareStreams(); err != nil {
		return nil, err
	}
	a.report = newReport(cfg, start, a.work)

	// On resume, restore the outputs of stages a previous run completed before
	// the runner skips them, so later stages see the same data. A fresh run
	// must never read a stale checkpoint: that would double-count records.
	if cfg.Resume {
		a.loadCompleted()
		a.loadManifest()
	}

	steps := a.steps()
	runner := stage.New(steps, stage.Options{
		StatePath:      ws.StatePath,
		Resume:         cfg.Resume,
		InterruptAfter: cfg.InterruptAfter,
	}, a.mf)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if deadline, ok := stage.BudgetDeadline(start, cfg.BudgetMS); ok {
		var stop context.CancelFunc
		runCtx, stop = context.WithDeadline(runCtx, deadline)
		defer stop()
	}

	outcome, runErr := runner.Run(runCtx)
	a.report.setOutcome(outcome)
	if runErr != nil {
		return a.report, runErr
	}
	// A resumed run may skip validate entirely; its recorded results are still
	// needed to print the item-by-item conclusions.
	for _, name := range outcome.Skipped {
		if name != "validate" || outcome.Interrupted != "" {
			continue
		}
		for _, w := range a.work {
			_ = a.ws.loadStage("validate", w.Name, &w.Validate)
		}
	}
	if outcome.Interrupted != "" {
		// The run stopped early on purpose. Progress up to the interruption is
		// persisted so the next run resumes at the first unfinished stage.
		a.report.finalizeInterrupted(a)
		return a.report, nil
	}

	if err := a.publishManifest(); err != nil {
		return a.report, err
	}
	a.report.finalize(a)
	return a.report, nil
}

// prepareStreams generates the self-test fixtures or opens the given stream.
func (a *app) prepareStreams() error {
	if a.cfg.SelfTest {
		dir := filepath.Join(a.ws.Dir, "input")
		normal, err := sample.Write(dir, sample.NormalSpec(), a.cfg.Seed)
		if err != nil {
			return fault.New(fault.StageIngest, "generate normal fixture", err)
		}
		faulty, err := sample.Write(dir, sample.FaultSpec(), a.cfg.Seed+1)
		if err != nil {
			return fault.New(fault.StageIngest, "generate fault fixture", err)
		}
		a.work = []*work{
			{Name: "normal"},
			{Name: "fault", ExpectDamage: true},
		}
		a.work[0].Stream.Path = normal.Path
		a.work[1].Stream.Path = faulty.Path
		a.work[0].Stream.TruncatedOffset = normal.TruncatedRTCPOffset
		a.work[1].Stream.TruncatedOffset = faulty.TruncatedRTCPOffset
	} else {
		a.work = []*work{{Name: "input"}}
		a.work[0].Stream.Path = a.cfg.StreamPath
	}

	for _, w := range a.work {
		info, err := os.Stat(w.Stream.Path)
		if err != nil {
			return fault.New(fault.StageIngest, "stat "+w.Name, err)
		}
		if info.Size() > ingest.MaxStreamBytes {
			return fault.At(fault.StageIngest, "size check", 0,
				fmt.Errorf("%w: %d bytes exceeds %d", fault.ErrLimit, info.Size(), ingest.MaxStreamBytes))
		}
		sha, err := validate.FileSHA256(w.Stream.Path)
		if err != nil {
			return err
		}
		w.Stream.SHA256 = sha
		w.Stream.Bytes = info.Size()
		w.Stream.TruncatedOffset = -1
		s, err := ingest.Open(w.Stream.Path)
		if err != nil {
			return err
		}
		w.Stream.StartUnixNano = s.Header.StartUnixNano
		s.Close()
	}
	a.buildManifest()
	return nil
}

// buildManifest creates the manifest skeleton with the stream digests.
func (a *app) buildManifest() {
	var streamDigests []string
	for _, w := range a.work {
		streamDigests = append(streamDigests, w.Stream.SHA256)
	}
	a.mf = manifest.New(a.cfg.Seed, a.cfg.BudgetMS, a.work[0].Stream.StartUnixNano, stageNames)
	a.mf.StreamSHA256 = digestStrings("streams", streamDigests...)
}

// loadManifest adopts the manifest of an earlier run when it describes the
// same inputs, so stage records survive a restart.
func (a *app) loadManifest() {
	data, err := os.ReadFile(a.ws.manifestPath())
	if err != nil {
		return
	}
	prev, err := manifest.Parse(data)
	if err != nil || prev.Seed != a.mf.Seed || prev.BudgetMS != a.mf.BudgetMS ||
		prev.StreamSHA256 != a.mf.StreamSHA256 {
		return
	}
	a.mf.Stages = prev.Stages
	a.mf.Artifacts = append([]manifest.Artifact(nil), prev.Artifacts...)
	// Reset the terminal stage only when it did not finish: it is about to run
	// again and will rewrite its own record. A finished validate stage keeps
	// its record so replaying an already complete run is a no-op.
	cp, _ := stage.LoadCheckpoint(a.ws.StatePath)
	validateDone := false
	if cp != nil {
		for _, name := range cp.Completed {
			if name == "validate" {
				validateDone = true
			}
		}
	}
	if !validateDone {
		for i := range a.mf.Stages {
			if a.mf.Stages[i].Stage == "validate" {
				a.mf.Stages[i] = manifest.StageRecord{Stage: "validate", Status: manifest.StatusPending}
			}
		}
		a.mf.Artifacts = nil
	}
}

// completedManifestAt returns the digest of the manifest on disk when it
// represents a complete run. An interrupted manifest is progress, not a
// deliverable, and is not compared against.
func completedManifestAt(ws *workspace) string {
	data, err := os.ReadFile(ws.manifestPath())
	if err != nil {
		return ""
	}
	m, err := manifest.Parse(data)
	if err != nil || m.ResumeStage != "complete" {
		return ""
	}
	return digest(data)
}

// loadCompleted restores stage outputs recorded as complete in the checkpoint.
// It never fails the run: a missing or unreadable file simply means the stage
// will be recomputed.
func (a *app) loadCompleted() {
	cp, err := stage.LoadCheckpoint(a.ws.StatePath)
	if err != nil {
		return
	}
	done := map[string]bool{}
	for _, name := range cp.Completed {
		done[name] = true
	}
	load := func(stageName string, assign func(*work) any) {
		if !done[stageName] {
			return
		}
		for _, w := range a.work {
			_ = a.ws.loadStage(stageName, w.Name, assign(w))
		}
	}
	load("ingest", func(w *work) any { return &w.Stream })
	load("reorder", func(w *work) any { return &w.Reorder })
	load("rtcp", func(w *work) any { return &w.RTCP })
	load("events", func(w *work) any { return &w.Events })
	load("align", func(w *work) any { return &w.Align })
	load("mux", func(w *work) any { return &w.Mux })
}

// steps returns the pipeline stages in order.
func (a *app) steps() []stage.Step {
	return []stage.Step{
		{Name: "ingest", Run: a.runIngest},
		{Name: "reorder", Run: a.runReorder},
		{Name: "rtcp", Run: a.runRTCP},
		{Name: "events", Run: a.runEvents},
		{Name: "align", Run: a.runAlign},
		{Name: "mux", Run: a.runMux},
		{Name: "validate", Run: a.runValidate},
	}
}

// finalizeStage records a stage in the manifest and persists its outputs.
func (a *app) finalizeStage(ctx context.Context, name string, params []string, note func() string) error {
	for _, w := range a.work {
		if err := a.ws.saveStage(name, w.Name, a.stageValue(name, w)); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil // the budget ended after the work was done; keep the result
	}
	inputDigest := a.inputDigest()
	paramDigest := digestStrings("params:"+name, append([]string{fmt.Sprintf("seed=%d", a.cfg.Seed)}, params...)...)
	status := manifest.StatusDone
	if a.stagePartial(name) {
		status = manifest.StatusPartial
	}
	a.mf.SetStageInput(name, inputDigest, paramDigest)
	outDigest := a.ws.stageDigest(name, a.work)
	stageNote := note()
	if name == "validate" {
		// The validate stage reports the manifest digest, so recording its own
		// output digest would make the manifest self-referential. Its inputs,
		// parameters and status are still recorded.
		outDigest = ""
	}
	a.mf.SetStage(name, status, outDigest, stageNote)
	a.mf.ComputeResumeStage()
	// Persist progress so an interrupted run can resume the manifest as well as
	// the stage outputs.
	return a.publishManifest()
}

func (a *app) stageValue(name string, w *work) any {
	switch name {
	case "ingest":
		return &w.Stream
	case "reorder":
		return &w.Reorder
	case "rtcp":
		return &w.RTCP
	case "events":
		return &w.Events
	case "align":
		return &w.Align
	case "mux":
		return &w.Mux
	case "validate":
		return &w.Validate
	}
	return nil
}

// stagePartial reports whether a stage recorded recoverable damage.
func (a *app) stagePartial(name string) bool {
	for _, w := range a.work {
		switch name {
		case "reorder":
			if w.Reorder.ParseErrors > 0 {
				return true
			}
		case "rtcp":
			if w.RTCP.Errors > 0 {
				return true
			}
		}
	}
	return false
}

// inputDigest summarizes the input of a stage: the stream digests plus every
// earlier stage output, so a stage cannot be replayed against changed input.
func (a *app) inputDigest() string {
	var parts []string
	for _, w := range a.work {
		parts = append(parts, w.Stream.SHA256)
	}
	return digestStrings("stage-input", parts...)
}

// recordSoft remembers recoverable input damage for the final classification.
func (a *app) recordSoft(err error) {
	a.softErrs = append(a.softErrs, err)
}

func (a *app) notePeak(bytes, packets int64) {
	if bytes > a.peakByte {
		a.peakByte = bytes
	}
	if packets > a.peakPkts {
		a.peakPkts = packets
	}
}

// runIngest reads every record once to count records and enforce the byte cap.
func (a *app) runIngest(ctx context.Context) error {
	for _, w := range a.work {
		if err := a.ingestOne(ctx, w); err != nil {
			return err
		}
	}
	return a.finalizeStage(ctx, "ingest",
		[]string{fmt.Sprintf("cap=%d", ingest.MaxStreamBytes), fmt.Sprintf("depth=%d", ingest.ChannelDepth)},
		func() string {
			info, err := os.Stat(a.work[0].Stream.Path)
			_ = info
			_ = err
			parts := ""
			for i, w := range a.work {
				if i > 0 {
					parts += " "
				}
				parts += fmt.Sprintf("%s:records=%d,rtp=%d,rtcp=%d,events=%d",
					w.Name, w.Stream.Records, w.Stream.RTPRecords, w.Stream.RTCPRecords, w.Stream.EventRecords)
			}
			return parts
		})
}

func (a *app) ingestOne(ctx context.Context, w *work) error {
	w.Stream.Records = 0
	w.Stream.RTPRecords = 0
	w.Stream.RTCPRecords = 0
	w.Stream.EventRecords = 0
	s, err := ingest.Open(w.Stream.Path)
	if err != nil {
		return err
	}
	defer s.Close()

	recs, errs := s.Emit(ctx)
	var pending int64
	for rec := range recs {
		w.Stream.Records++
		switch rec.Kind {
		case ingest.KindRTP:
			w.Stream.RTPRecords++
		case ingest.KindRTCP:
			w.Stream.RTCPRecords++
		case ingest.KindEvent:
			w.Stream.EventRecords++
		}
		pending = int64(len(rec.Data))
	}
	err = <-errs
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: %v", stage.ErrBudgetExceeded, err)
		}
		return err
	}
	if w.Stream.Records == 0 {
		return fault.At(fault.StageIngest, "read stream", 0, fmt.Errorf("%w: no records", fault.ErrCorrupt))
	}
	a.notePeak(w.Stream.Bytes+pending, int64(ingest.ChannelDepth))
	return nil
}

// runReorder parses, deduplicates and reorders RTP records.
func (a *app) runReorder(ctx context.Context) error {
	for _, w := range a.work {
		if err := a.reorderOne(ctx, w); err != nil {
			return err
		}
	}
	return a.finalizeStage(ctx, "reorder",
		[]string{fmt.Sprintf("window=%d", reorderWindow)},
		func() string {
			return fmt.Sprintf("unique=%d duplicates=%d loss=%d parse_errors=%d",
				a.totalInt(func(w *work) int { return len(w.Reorder.Packets) }),
				a.totalInt(func(w *work) int { return w.Reorder.Duplicates }),
				a.totalInt(func(w *work) int { return w.Reorder.LossTotal }),
				a.totalInt(func(w *work) int { return w.Reorder.ParseErrors }))
		})
}

func (a *app) reorderOne(ctx context.Context, w *work) error {
	s, err := ingest.Open(w.Stream.Path)
	if err != nil {
		return err
	}
	defer s.Close()

	r := rtp.NewReorderer(reorderWindow)
	var parseErrs int
	var firstErr error
	var maxRecord int64
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %v", stage.ErrBudgetExceeded, err)
		}
		rec, err := s.ReadRecord()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if rec.Kind != ingest.KindRTP {
			continue
		}
		if int64(len(rec.Data)) > maxRecord {
			maxRecord = int64(len(rec.Data))
		}
		if err := r.Push(rec.Data, rec.Offset); err != nil {
			parseErrs++
			if firstErr == nil {
				firstErr = err
			}
			a.recordSoft(err)
			continue // a single malformed packet never stops the stream
		}
	}
	res := r.Finish()
	w.parseErrors = parseErrs

	w.Reorder = reorderOut{
		Duplicates:     res.Duplicates,
		OutOfWindow:    res.OutOfWindow,
		TimestampWraps: res.TimestampWraps,
		PeakPending:    res.PeakPendingBytes,
		LossTotal:      res.LossesTotal(),
		LossRate:       res.LossRate(),
		ParseErrors:    parseErrs,
		FirstErrorOff:  -1,
	}
	if firstErr != nil {
		w.Reorder.FirstError = firstErr.Error()
		if off, ok := fault.OffsetOf(firstErr); ok {
			w.Reorder.FirstErrorOff = off
		}
	}
	for _, p := range res.Packets {
		w.Reorder.Packets = append(w.Reorder.Packets, packetJSON{
			Seq: p.Seq, ExtSeq: p.ExtSeq, TS: int64(p.TS), RawTS: p.RawTS,
			Marker: p.Marker, Payload: p.Payload, InputOff: p.InputOff, Suspect: p.TSSuspect,
		})
	}
	for _, l := range res.Losses {
		w.Reorder.Losses = append(w.Reorder.Losses, lossJSON{
			First: l.First, Last: l.Last, ExtFirst: l.ExtFirst, Count: l.Count,
		})
	}
	for _, an := range res.Anomalies {
		w.Reorder.Anomalies = append(w.Reorder.Anomalies, anomalyJSON{
			Seq: an.Seq, ExtSeq: an.ExtSeq, Offset: an.Offset, Reason: an.Reason,
		})
	}
	a.notePeak(w.Stream.Bytes+int64(res.PeakPendingBytes), int64(reorderWindow))
	_ = maxRecord
	return nil
}

// runRTCP parses report blocks and keeps structured truncation errors.
func (a *app) runRTCP(ctx context.Context) error {
	for _, w := range a.work {
		if err := a.rtcpOne(ctx, w); err != nil {
			return err
		}
	}
	return a.finalizeStage(ctx, "rtcp",
		[]string{"packets=sr,rr", "block_bytes=24"},
		func() string {
			return fmt.Sprintf("reports=%d blocks=%d errors=%d",
				a.totalInt(func(w *work) int { return w.RTCP.Reports }),
				a.totalInt(func(w *work) int { return w.RTCP.Blocks }),
				a.totalInt(func(w *work) int { return w.RTCP.Errors }))
		})
}

func (a *app) rtcpOne(ctx context.Context, w *work) error {
	s, err := ingest.Open(w.Stream.Path)
	if err != nil {
		return err
	}
	defer s.Close()

	rep := rtcpOut{TruncOffset: -1, Complete: true}
	var all []rtcp.Report
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %v", stage.ErrBudgetExceeded, err)
		}
		rec, err := s.ReadRecord()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if rec.Kind != ingest.KindRTCP {
			continue
		}
		reports, err := rtcp.ParseCompound(rec.Data, rec.PayloadOffset())
		if err != nil {
			rep.Errors++
			rep.Complete = false
			if rep.FirstError == "" {
				rep.FirstError = err.Error()
			}
			if off, ok := fault.OffsetOf(err); ok {
				rep.TruncOffset = off
			}
			a.recordSoft(err)
			continue // later records are still parsed
		}
		all = append(all, reports...)
	}
	sum := rtcp.Summarize(all)
	rep.Reports = len(all)
	rep.Blocks = sum.Blocks
	rep.LostTotal = sum.LostTotal
	rep.Jitter = sum.Jitter()
	rep.MaxJitter = sum.MaxJitter
	w.RTCP = rep
	return nil
}

// runEvents parses the client event records.
func (a *app) runEvents(ctx context.Context) error {
	for _, w := range a.work {
		if err := a.eventsOne(ctx, w); err != nil {
			return err
		}
	}
	return a.finalizeStage(ctx, "events",
		[]string{"formats=unix_ms|unix_s|rfc3339|relative"},
		func() string {
			return fmt.Sprintf("events=%d unalignable=%d",
				a.totalInt(func(w *work) int { return w.Events.Events }),
				a.totalInt(func(w *work) int { return w.Events.Unalignable }))
		})
}

func (a *app) eventsOne(ctx context.Context, w *work) error {
	s, err := ingest.Open(w.Stream.Path)
	if err != nil {
		return err
	}
	defer s.Close()

	var lines []byte
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %v", stage.ErrBudgetExceeded, err)
		}
		rec, err := s.ReadRecord()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if rec.Kind != ingest.KindEvent {
			continue
		}
		lines = append(lines, rec.Data...)
		lines = append(lines, '\n')
	}
	log := events.Parse(lines)
	w.EventLog = *log
	w.Events.Events = len(log.Events)
	w.Events.Unalignable = log.Unalignable
	return nil
}

// runAlign places events on the media timeline and derives stall intervals.
func (a *app) runAlign(ctx context.Context) error {
	for _, w := range a.work {
		if err := a.alignOne(ctx, w); err != nil {
			return err
		}
	}
	return a.finalizeStage(ctx, "align",
		[]string{fmt.Sprintf("threshold_ticks=%d", clock.GapThresholdTicks), "tick_rate=90000"},
		func() string {
			return fmt.Sprintf("events=%d intervals=%d unalignable=%d",
				a.totalInt(func(w *work) int { return len(w.Align.Events) }),
				a.totalInt(func(w *work) int { return len(w.Align.Intervals) }),
				a.totalInt(func(w *work) int { return w.Align.Unalignable }))
		})
}

func (a *app) alignOne(ctx context.Context, w *work) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %v", stage.ErrBudgetExceeded, err)
	}
	packets := make([]rtp.Packet, len(w.Reorder.Packets))
	for i, p := range w.Reorder.Packets {
		packets[i] = rtp.Packet{
			Seq: p.Seq, ExtSeq: p.ExtSeq, TS: clock.Tick(p.TS), RawTS: p.RawTS,
			Marker: p.Marker, Payload: p.Payload, InputOff: p.InputOff, TSSuspect: p.Suspect,
		}
	}
	log := w.EventLog
	res, err := align.Run(packets, &log, align.Config{
		MediaOrigin:        mediaOrigin(w),
		CaptureStartMillis: w.Stream.StartUnixNano / 1_000_000,
		HasCaptureStart:    w.Stream.StartUnixNano != 0,
	})
	if err != nil {
		return fault.New(fault.StageAlign, "align", err)
	}
	out := alignOut{Unalignable: res.Unalignable, Threshold: int64(res.Threshold)}
	for _, e := range res.Events {
		out.Events = append(out.Events, alignEventJSON{
			Line: e.Record.Line, Kind: string(e.Record.Kind), Media: int64(e.Media),
			Interval: e.Interval, Alignable: e.Record.Alignable, Reason: e.Record.Reason,
		})
	}
	for _, iv := range res.Intervals {
		out.Intervals = append(out.Intervals, intervalJSON{
			StartSeq: iv.StartSeq, EndSeq: iv.EndSeq,
			StartTime: int64(iv.StartTime), EndTime: int64(iv.EndTime),
			Gap: int64(iv.Gap), Events: iv.Events,
		})
	}
	w.Align = out
	return nil
}

// runMux writes reconstructed payloads into an MP4 track.
func (a *app) runMux(ctx context.Context) error {
	for _, w := range a.work {
		if err := a.muxOne(ctx, w); err != nil {
			return err
		}
	}
	return a.finalizeStage(ctx, "mux",
		[]string{fmt.Sprintf("timescale=%d", clock.TickRate), "container=mp4"},
		func() string {
			return fmt.Sprintf("samples=%d bytes=%d",
				a.totalInt(func(w *work) int { return w.Mux.Samples }),
				a.totalInt64(func(w *work) int64 { return w.Mux.Bytes }))
		})
}

func (a *app) muxOne(ctx context.Context, w *work) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %v", stage.ErrBudgetExceeded, err)
	}
	packets := make([]rtp.Packet, len(w.Reorder.Packets))
	for i, p := range w.Reorder.Packets {
		packets[i] = rtp.Packet{
			Seq: p.Seq, ExtSeq: p.ExtSeq, TS: clock.Tick(p.TS), RawTS: p.RawTS,
			Marker: p.Marker, Payload: p.Payload, InputOff: p.InputOff, TSSuspect: p.Suspect,
		}
	}
	if len(packets) == 0 {
		return fault.New(fault.StageMux, "write track",
			fmt.Errorf("%w: no reconstructed packets for %s", fault.ErrCorrupt, w.Name))
	}
	path := a.ws.stagedPath(w.Name)
	track, err := mp4.WriteTrack(path, packets, mp4.WriteOptions{})
	if err != nil {
		return err
	}
	sha, err := validate.FileSHA256(path)
	if err != nil {
		return err
	}
	w.Mux = muxOut{
		Artifact: filepath.Base(path), ArtifactSHA: sha,
		Samples: len(track.Samples), Duration: track.Duration, Bytes: track.Bytes,
	}
	return nil
}

// runValidate checks structure, hashes, memory, budget and thresholds.
//
// The validate stage record is fixed before the checks run: its note is a
// static list of the items, never their outcome. Validation results are part of
// the printed report, not part of the manifest, so the published manifest is
// byte-identical to the one the hash item verifies and does not depend on
// whether a previous run's manifest happened to match.
func (a *app) runValidate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %v", stage.ErrBudgetExceeded, err)
	}
	a.mf.Artifacts = nil
	for _, w := range a.work {
		a.mf.AddArtifact(w.Mux.Artifact, w.Mux.ArtifactSHA, w.Mux.Bytes)
	}
	a.mf.SortArtifacts()
	a.mf.SetStageInput("validate", a.inputDigest(), a.validateParamDigest())
	a.mf.SetStage("validate", manifest.StatusDone, "", "items="+strings.Join(validateItemNames, ","))
	a.mf.ComputeResumeStage()

	currentManifestSHA, err := a.mf.SHA256()
	if err != nil {
		return fault.New(fault.StageManifest, "digest manifest", err)
	}
	// A repeated delivery must reproduce the previous manifest byte for byte.
	if a.previousManifest != "" && a.previousManifest != currentManifestSHA {
		a.repeatedManifestMismatch = true
	}
	for _, w := range a.work {
		rep, err := validate.Run(validate.Inputs{
			ArtifactPath:        a.ws.stagedPath(w.Name),
			Manifest:            a.mf,
			ExpectedManifestSHA: currentManifestSHA,
			PreviousManifestSHA: a.previousManifest,
			ExpectedArtifactSHA: w.Mux.ArtifactSHA,
			ExpectedSamples:     len(w.Reorder.Packets),
			Gaps:                len(w.Align.Intervals),
			LossRate:            w.Reorder.LossRate,
			LossRateMax:         0.02,
			PeakBufferBytes:     a.peakByte,
			PeakPackets:         a.peakPkts,
			Budget: validate.Budget{
				MaxBufferBytes: validate.DefaultBufferBytes,
				MaxPackets:     validate.DefaultMaxPackets,
				BudgetMS:       a.cfg.BudgetMS,
				ElapsedMS:      time.Since(a.start).Milliseconds(),
			},
		})
		if err != nil {
			return err
		}
		out := validateOut{Passed: rep.Passed()}
		for _, item := range rep.Results {
			out.Items = append(out.Items, itemJSON{Name: item.Name, Pass: item.Pass, Detail: item.Detail})
		}
		out.Failures = len(rep.Failures())
		w.Validate = out
		if err := a.ws.saveStage("validate", w.Name, &w.Validate); err != nil {
			return err
		}
	}

	// Publish only when every structural and hash item passed. Threshold items
	// (loss, gap, budget) describe the input, not the artifact, so a PASS here
	// means "the bytes are trustworthy", not "the stream was healthy".
	var names []string
	publishable := true
	for _, w := range a.work {
		names = append(names, w.Name)
		for _, it := range w.Validate.Items {
			if it.Pass {
				continue
			}
			if isArtifactIntegrityItem(it.Name) {
				publishable = false
			}
		}
	}
	if a.repeatedManifestMismatch {
		publishable = false
	}
	if !publishable {
		a.ws.discard(names)
		a.unpublished = true
		return nil
	}
	for _, w := range a.work {
		if err := a.ws.publish(w.Name); err != nil {
			return err
		}
	}
	return nil
}

// isArtifactIntegrityItem reports whether a failed item means the artifact
// bytes themselves cannot be trusted.
func isArtifactIntegrityItem(name string) bool {
	switch name {
	case "mp4_sample_count", "mp4_dts_monotonic", "mp4_timescale", "artifact_hash",
		"manifest_hash", "manifest_repeatable", "memory_ceiling":
		return true
	}
	return false
}

// validateParamDigest is the deterministic parameter digest for the validate
// stage. It must not depend on the validation outcome.
func (a *app) validateParamDigest() string {
	values := []string{fmt.Sprintf("seed=%d", a.cfg.Seed), fmt.Sprintf("budget_ms=%d", a.cfg.BudgetMS)}
	values = append(values, validateItemNames...)
	return digestStrings("params:validate", values...)
}

// validateItemNames lists the validation items in their fixed order.
var validateItemNames = []string{
	"mp4_sample_count", "mp4_dts_monotonic", "mp4_timescale", "artifact_hash",
	"manifest_hash", "manifest_repeatable", "memory_ceiling", "time_budget",
	"loss_rate", "gap_intervals",
}

// publishManifest writes the final manifest atomically.
func (a *app) publishManifest() error {
	a.mf.ComputeResumeStage()
	data, err := a.mf.Bytes()
	if err != nil {
		return fault.New(fault.StageManifest, "encode manifest", err)
	}
	if err := writeAtomic(a.ws.manifestPath(), data); err != nil {
		return fault.New(fault.StageManifest, "publish manifest", err)
	}
	return nil
}

// mediaOrigin is the unwrapped timestamp of the first reconstructed packet. It
// is recoverable from the persisted packet list, so a resumed run uses exactly
// the same origin as the original run.
func mediaOrigin(w *work) clock.Tick {
	if len(w.Reorder.Packets) == 0 {
		return 0
	}
	return clock.Tick(w.Reorder.Packets[0].TS)
}

func (a *app) totalInt(fn func(*work) int) int {
	sum := 0
	for _, w := range a.work {
		sum += fn(w)
	}
	return sum
}

func (a *app) totalInt64(fn func(*work) int64) int64 {
	var sum int64
	for _, w := range a.work {
		sum += fn(w)
	}
	return sum
}
