// Package engine wires the parser, clock, segmenter, muxer and DAG into one
// bounded-memory processing run. Data flows through a fixed-capacity
// flow.Stream: a slow writer applies backpressure to the reader instead of
// buffering the whole input. Interruption points are honored between
// segments, and every finalized segment is committed through the checkpoint
// ledger so resume never double-submits.
package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mediad/internal/clock"
	"mediad/internal/dag"
	"mediad/internal/fixture"
	"mediad/internal/flow"
	"mediad/internal/model"
	"mediad/internal/segment"
	"mediad/internal/ts"
)

// QueueCapacity bounds the in-memory packet queue between reader and
// processor. Memory is therefore O(queue + open segments), never O(input).
const QueueCapacity = 32

// Report is the terminal summary printed by the CLI and embedded in runs.
type Report struct {
	SegmentCount        int      `json:"segment_count"`
	RepresentationCount int      `json:"representation_count"`
	MaxAlignDeltaTicks  int64    `json:"max_alignment_delta_ticks"`
	PacketTicks         int64    `json:"packet_ticks"`
	ManifestSHA256      string   `json:"manifest_sha256"`
	CorruptionOffset    int64    `json:"corruption_offset,omitempty"`
	AffectedPID         uint16   `json:"affected_pid,omitempty"`
	CorruptKinds        []string `json:"corrupt_kinds,omitempty"`
	ResumedFromSegment  int      `json:"resumed_from_segment,omitempty"`
	ResumeMs            int64    `json:"resume_ms,omitempty"`
}

// Config controls one run.
type Config struct {
	OutDir        string
	InterruptAt   string // e.g. "segment:2", empty disables
	CorruptMode   string
	// ReuseFixture lets tests inject a pre-generated stream; nil generates.
	Input *fixture.Stream
}

// repState holds the per-representation processing state. Routing follows
// the current PSI mapping dynamically, so it keeps no fixed media PID.
type repState struct {
	id       string
	timeline *clock.Timeline
	cutter   *segment.Cutter
	// samples accumulates only the packets of the currently open segment;
	// it is reset each time a segment is finalized and muxed.
	samples [][]byte
}

// Run executes one full or resumed processing run.
func Run(ctx context.Context, cfg Config) (*Report, error) {
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return nil, err
	}
	cpPath := filepath.Join(cfg.OutDir, "checkpoint.json")
	cp, loadErr := dag.LoadCheckpoint(cpPath)
	resuming := loadErr == nil
	start := time.Now()

	stream := cfg.Input
	if stream == nil {
		spec := fixture.DefaultSpec()
		spec.Corrupt = cfg.CorruptMode
		var err error
		if stream, err = fixture.Generate(spec); err != nil {
			return nil, err
		}
	}

	base := model.TimeBase{SegmentTicks: stream.SegmentTicks}
	muxer, err := segment.NewMuxer(cfg.OutDir)
	if err != nil {
		return nil, err
	}

	reps := make([]*repState, 2)
	repMeta := make([]segment.Representation, 2)
	for i := 0; i < 2; i++ {
		id := repID(i)
		tl := clock.NewTimeline()
		reps[i] = &repState{
			id:       id,
			timeline: tl,
			cutter:   segment.NewCutter(id, base, tl, stream.PacketTicks),
		}
		initName, err := muxer.WriteInit(id, uint32(model.ClockHz))
		if err != nil {
			return nil, err
		}
		repMeta[i] = segment.Representation{
			ID: id, BitrateBps: 400000 >> i,
			PacketTicks: stream.PacketTicks, InitFile: initName,
			SegmentPrefix: id,
		}
	}
	// routing holds the current PSI-derived PID lists. It is refreshed
	// whenever a PAT/PMT event changes the program mapping. It starts empty;
	// the head-of-stream PAT/PMT populates it before media packets arrive.
	routing := &routingState{streamTypeVideo: 0x1B}

	align := segment.NewAlign(base, stream.PacketTicks)

	// Resume: pre-seed every representation with the segments already in
	// the checkpoint ledger. Replay re-derives timelines from scratch (the
	// input is identical), but adopted segments are never re-muxed or
	// re-committed, which keeps output bit-for-bit identical.
	if resuming {
		for _, commit := range cp.Commits() {
			for _, r := range reps {
				if r.id == commit.Rep {
					r.cutter.Adopt(&segment.Descriptor{
						Representation: commit.Rep,
						Index:          commit.Index,
						StartTicks:     base.SegmentStart(commit.Index),
						EndTicks:       base.SegmentStart(commit.Index+1) - 1,
						ByteSize:       commit.ByteSize,
						SHA256:         commit.SHA256,
					})
				}
			}
		}
	}

	reader := ts.NewReader(bytes.NewReader(stream.Bytes))
	ct := ts.NewContinuityTracker()
	psi := ts.NewPSITracker()
	queue := flow.New[*model.Packet](QueueCapacity)

	var corruption []segment.CorruptionReport
	var firstCorrupt *model.CorruptError
	var corruptMu sync.Mutex
	// recordCorrupt stores every defect. Primary defects (sync, truncated,
	// continuity, pcr, malformed PSI sections) own the first-corrupt slot;
	// secondary "output paused" PSI blockers are recorded but never displace
	// an earlier primary defect, so the reported offset matches the actual
	// injected corruption position.
	recordCorrupt := func(ce *model.CorruptError) {
		corruptMu.Lock()
		defer corruptMu.Unlock()
		corruption = append(corruption, segment.CorruptionReport{
			Kind: ce.Kind, Offset: ce.Offset, PID: ce.PID, Detail: ce.Detail,
		})
		if firstCorrupt == nil {
			c := *ce
			firstCorrupt = &c
		}
	}
	recordBlocker := func(ce *model.CorruptError) {
		corruptMu.Lock()
		defer corruptMu.Unlock()
		corruption = append(corruption, segment.CorruptionReport{
			Kind: ce.Kind, Offset: ce.Offset, PID: ce.PID, Detail: ce.Detail,
		})
	}

	// Producer goroutine: parse packets and push through the bounded queue.
	// Parser-level defects (bad sync byte, truncated tail) end the input
	// stream but are first recorded as in-band corruption so the run still
	// emits a manifest describing every segment produced before the defect.
	// stop is closed when the consumer finishes (including interruption),
	// which unblocks a producer waiting on a full queue so the run cannot
	// deadlock when it stops before draining the whole input.
	stop := make(chan struct{})
	prodErr := make(chan error, 1)
	go func() {
		defer queue.CloseWithError(nil)
		for {
			pkt, err := reader.ReadPacket(ctx)
			if errors.Is(err, io.EOF) {
				prodErr <- nil
				return
			}
			if err != nil {
				var ce *model.CorruptError
				if errors.As(err, &ce) {
					recordCorrupt(ce)
					prodErr <- nil
					return
				}
				queue.CloseWithError(err)
				prodErr <- err
				return
			}
			select {
			case <-stop:
				prodErr <- nil
				return
			default:
			}
			if err := queue.SendUntilClose(ctx, pkt, stop); err != nil {
				if errors.Is(err, flow.ErrStopped) {
					prodErr <- nil
					return
				}
				prodErr <- err
				return
			}
		}
	}()

	consumerErr := runConsumer(ctx, cfg, queue, ct, psi, routing, reps, stream, base, align, muxer, cp, recordCorrupt, recordBlocker)
	close(stop) // release a producer potentially blocked on the full queue
	pErr := <-prodErr
	cErr := consumerErr
	if err := errors.Join(pErr, cErr); err != nil && !errors.Is(err, errInterrupt) {
		return nil, classifyFailure(err)
	}

	// Flush the open segment of every representation at clean EOF. An
	// interrupted run deliberately leaves the open segment uncommitted and
	// writes no manifest: resume replays and finishes the remaining work.
	if errors.Is(cErr, errInterrupt) {
		if err := cp.MarkNode("interrupted", dag.StateSucceeded); err != nil {
			return nil, err
		}
		return &Report{
			SegmentCount:        committedSegmentCount(cp),
			RepresentationCount: len(repMeta),
			MaxAlignDeltaTicks:  align.MaxDriftTicks(),
			PacketTicks:         stream.PacketTicks,
			CorruptionOffset:    firstCorruptOffset(firstCorrupt),
			AffectedPID:         firstCorruptPID(firstCorrupt),
		}, ErrInterrupted
	}
	for _, r := range reps {
		if d := r.cutter.Flush(); d != nil {
			if err := finalizeSegment(r, d, muxer, cp, base); err != nil {
				return nil, err
			}
		}
	}

	if err := align.Validate(); err != nil {
		return nil, err
	}

	// Build the manifest from the checkpoint ledger plus descriptors.
	manifest, err := buildManifest(base, repMeta, reps, align, corruption, cp)
	if err != nil {
		return nil, err
	}
	manifestRaw, err := manifest.Marshal()
	if err != nil {
		return nil, err
	}
	manifestPath := filepath.Join(cfg.OutDir, "manifest.json")
	if err := atomicWrite(manifestPath, manifestRaw); err != nil {
		return nil, err
	}
	manifestSHA, _ := manifest.SHA256()

	report := &Report{
		SegmentCount:        manifest.SegmentCount,
		RepresentationCount: len(manifest.Representations),
		MaxAlignDeltaTicks:  align.MaxDriftTicks(),
		PacketTicks:         stream.PacketTicks,
		ManifestSHA256:      manifestSHA,
	}
	if firstCorrupt != nil {
		report.CorruptionOffset = firstCorrupt.Offset
		report.AffectedPID = firstCorrupt.PID
		seen := map[string]bool{}
		for _, c := range corruption {
			if !seen[c.Kind] {
				seen[c.Kind] = true
				report.CorruptKinds = append(report.CorruptKinds, c.Kind)
			}
		}
	}
	if resuming {
		report.ResumedFromSegment = firstResumableSegment(cp)
		report.ResumeMs = time.Since(start).Milliseconds()
	}
	// Persist a node-level marker so the DAG view shows completion.
	if err := cp.MarkNode("manifest", dag.StateSucceeded); err != nil {
		return nil, err
	}
	return report, nil
}

// errInterrupt signals the configured interruption point was reached.
var errInterrupt = errors.New("engine: interrupted at configured boundary")
