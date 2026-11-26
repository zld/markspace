package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"archivecheck/cache"
	"archivecheck/checkpoint"
	"archivecheck/manifest"
	"archivecheck/metrics"
	"archivecheck/pipe"
	rtpingest "archivecheck/rtp"
)

// Run executes or resumes the archive kernel in cfg.WorkDir.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	if cfg.WorkDir == "" {
		return nil, fmt.Errorf("state: WorkDir is required")
	}
	if cfg.Queue <= 0 {
		cfg.Queue = 2
	}
	if cfg.MemoryBytes <= 0 {
		cfg.MemoryBytes = 64 << 20
	}
	e := &engine{
		cfg:       cfg,
		root:      cfg.WorkDir,
		budget:    pipe.NewBudget(cfg.MemoryBytes),
		tracker:   rtpingest.NewTracker(),
		byCursor:  make(map[int64]workItem),
		itemIndex: make(map[int64]int),
	}
	if err := os.MkdirAll(e.root, 0o755); err != nil {
		return nil, err
	}

	var err error
	e.prior, err = Replay(ctx, filepath.Join(e.root, "journal"), e.budget)
	if err != nil {
		return nil, err
	}
	if e.prior != nil && e.prior.Found {
		e.cp = e.prior.Checkpoint
	}
	e.journal, err = checkpoint.Open(filepath.Join(e.root, "journal"))
	if err != nil {
		return nil, err
	}
	e.cas, err = cache.New(filepath.Join(e.root, "cas"))
	if err != nil {
		return nil, err
	}
	if err := e.loadInputs(); err != nil {
		return nil, err
	}
	if e.cp.InputDigest != "" && e.cp.InputDigest != e.inputDigest {
		return nil, fmt.Errorf("input digest changed: checkpoint=%s current=%s", e.cp.InputDigest, e.inputDigest)
	}
	e.loadArchive()
	e.buildWork()

	st := pipe.NewStream(pipe.Config{Queue: cfg.Queue, Budget: e.budget}, e.handle)
	st.Start()
	for _, wi := range e.work {
		if wi.rec.Cursor <= e.cp.Cursor {
			continue
		}
		if err := st.Send(ctx, wi.rec); err != nil {
			_ = st.Close()
			return e.partialResult(st), err
		}
	}
	if err := st.Close(); err != nil {
		return e.partialResult(st), err
	}

	archiveSHA, err := e.finishArchive(ctx)
	if err != nil {
		return nil, err
	}
	totals := e.tracker.Totals()
	rateText, ratePct := metrics.RecoverRate(int(totals.Recovered), int(totals.LossDetected))
	syncNS := metrics.MaxAVSyncNS(e.audioPTS, e.videoPTS)
	peak := e.budget.PeakBytes()
	if st.PeakInFlightBytes() > peak {
		peak = st.PeakInFlightBytes()
	}
	return &Result{
		Report: metrics.Report{
			OK: true, SyncMS: float64(syncNS) / 1e6,
			Loss: int(totals.LossDetected), Recovered: int(totals.Recovered),
			RecoverRate: rateText, RecoverRatePct: ratePct, Duplicate: int(totals.Duplicates),
			BadSegments: e.badSegments, Fallback: e.fallback, Unresolved: e.unresolved,
			Resumed: e.prior.Resumed, ArchiveSHA256: archiveSHA,
			P95MS:             float64(st.P95Latency().Microseconds()) / 1000.0,
			InFlightPeakBytes: peak, Events: len(e.events),
			CheckpointCursor: int64(len(e.work)),
		},
		ArchivePath: e.archivePath(), ArchiveSHA256: archiveSHA,
		CompleteThroughHLS:   e.hlsNext - 1,
		DASHGroupsResolved:   e.dashResolved,
		PeakInFlightBytes:    peak,
		LastCheckpointCursor: int64(len(e.work)),
	}, nil
}

func (e *engine) finishArchive(ctx context.Context) (string, error) {
	if err := e.writeArchive(); err != nil {
		return "", err
	}
	archiveBytes, err := os.ReadFile(e.archivePath())
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(archiveBytes)
	archiveSHA := hex.EncodeToString(sum[:])
	return archiveSHA, e.journal.Save(ctx, checkpoint.Checkpoint{
		InputDigest: e.inputDigest, Stage: checkpoint.StageCompleted,
		Cursor: int64(len(e.work)), ArchiveSHA256: archiveSHA,
	})
}

func (e *engine) loadInputs() error {
	dashBytes, err := os.ReadFile(filepath.Join(e.root, "manifest.mpd"))
	if err != nil {
		return err
	}
	hlsBytes, err := os.ReadFile(filepath.Join(e.root, "index.m3u8"))
	if err != nil {
		return err
	}
	eventsBytes, err := os.ReadFile(filepath.Join(e.root, "events.jsonl"))
	if err != nil {
		return err
	}
	h := sha256.New()
	_, _ = h.Write(dashBytes)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(hlsBytes)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(eventsBytes)
	e.inputDigest = hex.EncodeToString(h.Sum(nil))

	e.dash, err = manifest.ParseDASH(dashBytes, "manifest.mpd")
	if err != nil {
		return err
	}
	e.hls, err = manifest.ParseHLS(hlsBytes, "index.m3u8")
	if err != nil {
		return err
	}
	e.events, err = rtpingest.DecodeJSONL(bytes.NewReader(eventsBytes), "events.jsonl")
	return err
}

func (e *engine) buildWork() {
	cursor := int64(1)
	sort.Slice(e.dash.Representations, func(i, j int) bool {
		return e.dash.Representations[i].Bandwidth > e.dash.Representations[j].Bandwidth
	})
	reps := e.dash.Representations
	if len(reps) > 0 {
		for idx := range reps[0].Segments {
			wi := workItem{dashIndex: idx}
			wi.rec = pipe.Record{
				Kind: "dash-group", Source: fmt.Sprintf("dash-timeline-%d", idx),
				SizeBytes: e.groupSize(reps, idx),
			}
			e.addWork(cursor, wi)
			cursor++
		}
	}
	for idx, seg := range e.hls.Segments {
		wi := workItem{hlsIndex: idx}
		wi.rec = pipe.Record{Kind: "hls-segment", Source: seg.URI, SizeBytes: e.fileSize(seg.URI)}
		e.addWork(cursor, wi)
		cursor++
	}
	for i := range e.events {
		ev := &e.events[i]
		wi := workItem{event: ev}
		wi.rec = pipe.Record{Kind: "rtp-event", Source: ev.Source + "/" + ev.Track, SizeBytes: 512}
		e.addWork(cursor, wi)
		cursor++
	}
}

func (e *engine) addWork(cursor int64, wi workItem) {
	wi.rec.Cursor = cursor
	e.work = append(e.work, wi)
	e.byCursor[cursor] = wi
}

func (e *engine) groupSize(reps []manifest.Representation, idx int) int64 {
	var n int64 = 1
	for _, rep := range reps {
		if idx < len(rep.Segments) {
			n += e.fileSize(rep.Segments[idx].URL)
		}
	}
	return n
}

func (e *engine) fileSize(rel string) int64 {
	if st, err := os.Stat(filepath.Join(e.root, filepath.FromSlash(rel))); err == nil {
		return st.Size()
	}
	return 4096
}

func (e *engine) partialResult(st *pipe.Stream) *Result {
	cursor, _ := st.LastCursor()
	return &Result{
		PeakInFlightBytes: st.PeakInFlightBytes(), LastCheckpointCursor: cursor,
		CompleteThroughHLS: e.hlsNext - 1,
	}
}
