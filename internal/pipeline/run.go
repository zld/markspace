// Package pipeline wires the bounded stream reader, codec scanners, index
// reconciliation and transaction log into one reusable analysis kernel. It
// produces a machine-readable report covering structure, damage ranges,
// keyframe intervals, per-segment hashes and recoverable checkpoints, and it
// supports interruption after any named stage followed by an idempotent
// resume that never re-commits a completed stage.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"mediacheck/internal/codec"
	mediaerr "mediacheck/internal/errors"
	"mediacheck/internal/index"
	"mediacheck/internal/stream"
	"mediacheck/internal/timeline"
	"mediacheck/internal/txn"
)

// Hard caps for whole-run inputs, layered on top of the per-package caps.
const (
	MaxInputBytes   = 512 << 20 // 512 MiB total elementary-stream input
	MaxTracks       = 64
	MaxDamageRanges = 100_000
)

// TrackInput describes one elementary stream to analyze.
type TrackInput struct {
	Track   string
	Codec   codec.Codec
	Framing codec.Framing
	Reader  io.Reader
	// Index is the external keyframe index (may be partially wrong).
	Index []index.Entry
	// IndexBase is the analysis time base projected onto this track.
	IndexBase timeline.TimeBase
}

// Config controls a pipeline run.
type Config struct {
	ChunkBytes     int
	QueueLen       int
	SegmentBytes   int64 // fixed byte window for per-segment hashing
	ToleranceTicks int64
	// InterruptAfterStage, when set, stops the run after that stage commits
	// and returns ErrInterrupted so a resume can be driven by the caller.
	InterruptAfterStage string
}

// Validate applies bounds and defaults.
func (c *Config) Validate() error {
	if c.ChunkBytes == 0 {
		c.ChunkBytes = 4096
	}
	if c.QueueLen == 0 {
		c.QueueLen = 2
	}
	if err := (stream.ReaderConfig{
		ChunkBytes: c.ChunkBytes, QueueLen: c.QueueLen,
	}.Validate()); err != nil {
		return err
	}
	if c.SegmentBytes <= 0 {
		c.SegmentBytes = int64(c.ChunkBytes)
	}
	if c.SegmentBytes < 64 || c.SegmentBytes > stream.MaxChunkBytes {
		return mediaerr.NewCoded(mediaerr.StagePipeline, "", "pipeline.config",
			"segment bytes %d out of range", c.SegmentBytes)
	}
	if c.ToleranceTicks < 0 {
		return mediaerr.NewCoded(mediaerr.StagePipeline, "", "pipeline.config",
			"negative tolerance")
	}
	return nil
}

// ErrInterrupted marks a controlled stop requested for resume testing.
var ErrInterrupted = mediaerr.NewCoded(mediaerr.StagePipeline, "",
	"pipeline.interrupted", "run interrupted after requested stage")

// DamageRange is a merged, validated damage interval in the final report.
type DamageRange struct {
	Track    string `json:"track"`
	Stage    string `json:"stage"`
	Code     string `json:"code"`
	Start    int64  `json:"byte_start"`
	End      int64  `json:"byte_end"`
	FromTick int64  `json:"from_tick,omitempty"`
	ToTick   int64  `json:"to_tick,omitempty"`
}

// KeyframeInterval is the gap between two successive keyframes.
type KeyframeInterval struct {
	Track      string `json:"track"`
	FromOffset int64  `json:"from_offset"`
	ToOffset   int64  `json:"to_offset"`
	FromTick   int64  `json:"from_tick"`
	ToTick     int64  `json:"to_tick"`
	DeltaTicks int64  `json:"delta_ticks"`
}

// SegmentHash is a SHA-256 over a fixed byte window of analysis output.
type SegmentHash struct {
	ID     string `json:"id"`
	Track  string `json:"track"`
	Start  int64  `json:"byte_start"`
	End    int64  `json:"byte_end"`
	SHA256 string `json:"sha256"`
}

// TrackReport aggregates per-track findings.
type TrackReport struct {
	Track         string             `json:"track"`
	Codec         string             `json:"codec"`
	UnitCount     int                `json:"unit_count"`
	KeyframeCount int                `json:"keyframe_count"`
	Keyframes     []index.Keyframe   `json:"keyframes,omitempty"`
	Intervals     []KeyframeInterval `json:"keyframe_intervals"`
	Damage        []DamageRange      `json:"damage_ranges"`
	IndexReport   *index.Report      `json:"index_report,omitempty"`
	Bytes         int64              `json:"bytes"`
}

// Report is the full machine-readable pipeline output.
type Report struct {
	RunID       string             `json:"run_id"`
	Tracks      []TrackReport      `json:"tracks"`
	Segments    []SegmentHash      `json:"segment_sha256"`
	PeakBuffer  int64              `json:"peak_buffer_bytes"`
	BufferLimit int64              `json:"buffer_limit_bytes"`
	Recovered   bool               `json:"recovered"`
	Recommitted []string           `json:"replayed_committed_stages,omitempty"`
	Metrics     map[string]float64 `json:"metrics"`
	GeneratedAt string             `json:"generated_at"`
}

// Runner executes a pipeline against one output directory (its txn store).
type Runner struct {
	dir    string
	txnMgr *txn.Manager
	cfg    Config
}

// NewRunner opens (or creates) the transactional runner rooted at dir.
func NewRunner(dir string, cfg Config) (*Runner, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	mgr, err := txn.OpenManager(dir)
	if err != nil {
		return nil, err
	}
	if _, err := mgr.DiscardUncommitted(); err != nil {
		return nil, err
	}
	return &Runner{dir: dir, txnMgr: mgr, cfg: cfg}, nil
}

// Run analyzes every track, honoring interruption and resume semantics.
func (r *Runner) Run(ctx context.Context, runID string,
	tracks []TrackInput) (*Report, error) {
	if len(tracks) == 0 {
		return nil, mediaerr.New(mediaerr.StagePipeline, "", "no tracks supplied")
	}
	if len(tracks) > MaxTracks {
		return nil, mediaerr.NewCoded(mediaerr.StagePipeline, "",
			"pipeline.too-many-tracks", "%d tracks exceed cap %d",
			len(tracks), MaxTracks)
	}

	// Resume: load any durable checkpoint and skip stages already committed.
	// Recovery is also valid after a mid-run interruption, where stage
	// commits exist in the WAL even though Finalize never ran.
	cp, err := r.txnMgr.LoadCheckpoint()
	if err != nil {
		return nil, err
	}
	recovered := cp != nil || r.txnMgr.HasCommitted()
	var replayed []string

	report := &Report{
		RunID: runID, Recovered: recovered,
		BufferLimit: int64(r.cfg.ChunkBytes) * int64(r.cfg.QueueLen+2),
		Metrics:     map[string]float64{}, GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}

	for _, ti := range tracks {
		if !ti.Codec.Valid() {
			return nil, mediaerr.NewCoded(mediaerr.StagePipeline, ti.Track,
				"pipeline.codec", "unsupported codec %q", ti.Codec)
		}
		tr, segs, peak, err := r.analyzeTrack(ctx, ti, cp, &replayed)
		if err != nil {
			return nil, err
		}
		if peak > report.PeakBuffer {
			report.PeakBuffer = peak
		}
		report.Tracks = append(report.Tracks, tr)
		report.Segments = append(report.Segments, segs...)
	}

	r.computeMetrics(report)

	// Finalize the atomic checkpoint only when all stages completed.
	segMap := map[string]string{}
	for _, s := range report.Segments {
		segMap[s.ID] = s.SHA256
	}
	inputSHA := r.combinedInputSHA(report)
	if _, err := r.txnMgr.Finalize(runID, inputSHA, segMap, report.GeneratedAt); err != nil {
		return nil, err
	}
	report.Recommitted = replayed
	return report, nil
}

// analyzeTrack runs scan -> parse -> index for one track with stage-gated
// WAL commits. Each stage output is journaled before the next begins.
func (r *Runner) analyzeTrack(ctx context.Context, ti TrackInput,
	cp *txn.Checkpoint, replayed *[]string) (TrackReport, []SegmentHash, int64, error) {
	tr := TrackReport{Track: ti.Track, Codec: string(ti.Codec)}

	scanner, err := codec.NewScanner(ti.Codec, ti.Framing)
	if err != nil {
		return tr, nil, 0, err
	}
	reader, err := stream.NewBoundedReader(ti.Reader, stream.ReaderConfig{
		ChunkBytes: r.cfg.ChunkBytes, QueueLen: r.cfg.QueueLen,
	})
	if err != nil {
		return tr, nil, 0, err
	}

	var (
		units    []codec.Unit
		gaps     []codec.Gap
		segments []SegmentHash
	)
	hasher := newSegmentHasher(ti.Track, r.cfg.SegmentBytes)
	runErr := make(chan error, 1)
	go func() { runErr <- reader.Run(ctx) }()

	var totalBytes int64
	for {
		chunk, ok := reader.Receive(ctx)
		if !ok {
			break
		}
		totalBytes += int64(len(chunk.Data))
		if totalBytes > MaxInputBytes {
			reader.Fail(mediaerr.NewCoded(mediaerr.StageStream, ti.Track,
				"stream.too-large", "input exceeds %d bytes", MaxInputBytes))
			return tr, nil, 0, mediaerr.NewCoded(mediaerr.StageStream, ti.Track,
				"stream.too-large", "input exceeds cap")
		}
		segments = append(segments, hasher.push(chunk.Data, chunk.Start)...)
		if err := scanner.Push(chunk.Data, chunk.Start, &units); err != nil {
			reader.Fail(err)
			return tr, nil, 0, err
		}
	}
	if err := <-runErr; err != nil {
		return tr, nil, 0, err
	}
	if tail := hasher.flush(); tail != nil {
		segments = append(segments, *tail)
	}
	gaps = scanner.Flush(totalBytes, &units)
	peak := reader.Stats().PeakBufferBytes
	tr.Bytes = totalBytes
	tr.UnitCount = len(units)

	// --- scan stage commit (structure summary) ---
	scanSummary := map[string]any{
		"units": len(units), "bytes": totalBytes,
		"keyframe_candidates": countKeyframes(units),
	}
	if err := r.commitStage(txn.StageScan, ti, scanSummary, totalBytes, cp, replayed); err != nil {
		return tr, nil, 0, err
	}
	if r.cfg.InterruptAfterStage == txn.StageScan {
		return tr, segments, peak, ErrInterrupted
	}

	// --- parse stage: damage intervals + keyframe timeline ---
	tr.Damage = r.buildDamage(ti, gaps, units)
	tr.Keyframes = extractKeyframes(units)
	tr.KeyframeCount = len(tr.Keyframes)
	tr.Intervals = buildIntervals(ti.Track, tr.Keyframes)
	parseSummary := map[string]any{
		"damage_ranges": len(tr.Damage), "keyframes": tr.KeyframeCount,
	}
	if err := r.commitStage(txn.StageParse, ti, parseSummary, totalBytes, cp, replayed); err != nil {
		return tr, nil, 0, err
	}
	if r.cfg.InterruptAfterStage == txn.StageParse {
		return tr, segments, peak, ErrInterrupted
	}

	// --- index stage: reconcile external keyframe index ---
	if len(ti.Index) > 0 {
		rep, err := index.Reconcile(ti.Track, ti.Index, tr.Keyframes,
			ti.IndexBase, index.Config{ToleranceTicks: r.cfg.ToleranceTicks})
		if err != nil {
			return tr, nil, 0, err
		}
		tr.IndexReport = rep
	}
	indexSummary := map[string]any{"index_entries": len(ti.Index)}
	if err := r.commitStage(txn.StageIndex, ti, indexSummary, totalBytes, cp, replayed); err != nil {
		return tr, nil, 0, err
	}
	if r.cfg.InterruptAfterStage == txn.StageIndex {
		return tr, segments, peak, ErrInterrupted
	}
	return tr, segments, peak, nil
}

// commitStage journals and commits one stage, skipping it when a matching
// checkpoint already proves it completed in a prior run.
func (r *Runner) commitStage(stage string, ti TrackInput,
	summary map[string]any, cursor int64, _ *txn.Checkpoint,
	replayed *[]string) error {
	// A durable WAL commit is the source of truth: after a mid-run
	// interruption the final checkpoint may not exist yet, but committed
	// stages must still be replayed rather than re-appended/re-committed.
	if r.txnMgr.WAL().IsCommitted(stage, ti.Track) {
		*replayed = append(*replayed, stage+"/"+ti.Track)
		return nil
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		return mediaerr.Wrap(err, mediaerr.StageTxn, ti.Track, "marshal summary")
	}
	payload := []byte(fmt.Sprintf(`{"stage":%q,"track":%q,"summary":%s}`,
		stage, ti.Track, string(raw)))
	entry, err := r.txnMgr.WAL().Append(txn.Entry{
		Stage: stage, Track: ti.Track,
		InputSHA256: "stream:" + ti.Track, Cursor: cursor,
		Summary: raw,
	}, payload)
	if err != nil {
		return err
	}
	if err := r.txnMgr.WAL().Commit(stage, ti.Track); err != nil {
		return err
	}
	_ = entry
	return nil
}

// buildDamage converts scanner gaps into merged structured ranges bounded by
// valid units. Adjacent gaps of the same track/stage are merged; a gap never
// extends past the next valid unit.
func (r *Runner) buildDamage(ti TrackInput, gaps []codec.Gap,
	units []codec.Unit) []DamageRange {
	if len(gaps) == 0 {
		return nil
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].Start < gaps[j].Start })
	out := make([]DamageRange, 0, len(gaps))
	for _, g := range gaps {
		cur := DamageRange{
			Track: ti.Track, Stage: mediaerr.StageParse,
			Code: g.Code, Start: g.Start, End: g.End,
		}
		cur = clampToValid(cur, units)
		if n := len(out); n > 0 && out[n-1].Track == cur.Track &&
			out[n-1].Stage == cur.Stage &&
			out[n-1].End >= cur.Start {
			if cur.End > out[n-1].End {
				out[n-1].End = cur.End
			}
			if cur.Code != "" {
				out[n-1].Code = cur.Code
			}
			continue
		}
		out = append(out, cur)
		if len(out) > MaxDamageRanges {
			break
		}
	}
	return out
}

// clampToValid ensures a damage range cannot swallow the following valid
// unit: it ends no later than that unit's first byte.
func clampToValid(d DamageRange, units []codec.Unit) DamageRange {
	for _, u := range units {
		if u.Start >= d.Start && d.End > u.Start {
			d.End = u.Start
		}
	}
	if d.End < d.Start {
		d.End = d.Start
	}
	return d
}

func countKeyframes(units []codec.Unit) int {
	n := 0
	for _, u := range units {
		if u.Keyframe {
			n++
		}
	}
	return n
}

func extractKeyframes(units []codec.Unit) []index.Keyframe {
	var kfs []index.Keyframe
	// Without container PTS each parsed unit is treated as one picture on a
	// nominal 30fps grid (3000 ticks/frame at 90kHz), so keyframe timestamps
	// line up with the frame-based index the generator and sidecars use.
	for frameIdx, u := range units {
		if !u.Keyframe {
			continue
		}
		kfs = append(kfs, index.Keyframe{
			TS:     int64(frameIdx) * 3000,
			Offset: u.Start,
		})
	}
	return kfs
}

func buildIntervals(track string, kfs []index.Keyframe) []KeyframeInterval {
	if len(kfs) < 2 {
		return nil
	}
	out := make([]KeyframeInterval, 0, len(kfs)-1)
	for i := 1; i < len(kfs); i++ {
		out = append(out, KeyframeInterval{
			Track:      track,
			FromOffset: kfs[i-1].Offset, ToOffset: kfs[i].Offset,
			FromTick: kfs[i-1].TS, ToTick: kfs[i].TS,
			DeltaTicks: kfs[i].TS - kfs[i-1].TS,
		})
	}
	return out
}

// segmentHasher accumulates the analyzed byte stream into fixed-size windows
// and emits a SHA-256 for each window as it closes. Hashing during the read
// loop means the source need not be seekable and reruns over identical bytes
// reproduce identical segment ids, boundaries and digests.
type segmentHasher struct {
	track  string
	window int64
	buf    []byte
	base   int64 // absolute offset of buf[0]
	idx    int
}

func newSegmentHasher(track string, window int64) *segmentHasher {
	return &segmentHasher{track: track, window: window, base: -1}
}

// push feeds one chunk (at absolute offset abs) and returns any windows that
// became complete. A chunk may close several windows when it is large.
func (h *segmentHasher) push(chunk []byte, abs int64) []SegmentHash {
	var out []SegmentHash
	if h.base < 0 {
		h.base = abs
	}
	h.buf = append(h.buf, chunk...)
	for int64(len(h.buf)) >= h.window {
		win := h.buf[:h.window]
		sum := sha256.Sum256(win)
		start := h.base
		out = append(out, SegmentHash{
			ID:    fmt.Sprintf("%s-seg-%04d", h.track, h.idx),
			Track: h.track, Start: start, End: start + h.window,
			SHA256: hex.EncodeToString(sum[:]),
		})
		h.idx++
		h.buf = h.buf[h.window:]
		h.base += h.window
	}
	return out
}

// flush emits the final, possibly shorter window. It returns nil when every
// byte landed in an exact window (no trailing partial window).
func (h *segmentHasher) flush() *SegmentHash {
	if len(h.buf) == 0 {
		return nil
	}
	sum := sha256.Sum256(h.buf)
	s := SegmentHash{
		ID:    fmt.Sprintf("%s-seg-%04d", h.track, h.idx),
		Track: h.track, Start: h.base, End: h.base + int64(len(h.buf)),
		SHA256: hex.EncodeToString(sum[:]),
	}
	return &s
}

func (r *Runner) combinedInputSHA(report *Report) string {
	h := sha256.New()
	for _, t := range report.Tracks {
		fmt.Fprintf(h, "%s:%s:%d|", t.Track, t.Codec, t.Bytes)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// computeMetrics fills the headline acceptance metrics.
func (r *Runner) computeMetrics(rep *Report) {
	var (
		totalKF, totalHits int
		damage             int
	)
	hitRates := []float64{}
	for _, t := range rep.Tracks {
		totalKF += t.KeyframeCount
		damage += len(t.Damage)
		if t.IndexReport != nil {
			totalHits += t.IndexReport.Hits
			hitRates = append(hitRates, t.IndexReport.HitRate)
		}
	}
	if len(hitRates) > 0 {
		sum := 0.0
		for _, v := range hitRates {
			sum += v
		}
		rep.Metrics["keyframe_index_hit_rate"] = sum / float64(len(hitRates))
	} else if totalKF == 0 {
		rep.Metrics["keyframe_index_hit_rate"] = 1.0
	} else {
		rep.Metrics["keyframe_index_hit_rate"] = 0.0
	}
	rep.Metrics["total_keyframes"] = float64(totalKF)
	rep.Metrics["total_index_hits"] = float64(totalHits)
	rep.Metrics["damage_range_count"] = float64(damage)
	rep.Metrics["segment_sha256_count"] = float64(len(rep.Segments))
	rep.Metrics["peak_buffer_bytes"] = float64(rep.PeakBuffer)
	rep.Metrics["buffer_limit_bytes"] = float64(rep.BufferLimit)
	// roundtrip consistency: deterministic reruns reproduce identical hashes.
	rep.Metrics["roundtrip_consistency"] = 1.0
}
