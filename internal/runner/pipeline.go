package runner

import (
	"errors"
	"io"
	"path/filepath"

	"mediasync/internal/index"
	"mediasync/internal/metrics"
	"mediasync/internal/model"
	"mediasync/internal/provenance"
	"mediasync/internal/rtp"
	"mediasync/internal/sync"
)

// Params captures everything that makes a run reproducible.
type Params struct {
	ReorderWindow int          `json:"reorder_window"`
	ClockHz       uint32       `json:"clock_hz"`
	SSRC          uint32       `json:"ssrc"`
	SyncBudgetNS  model.Ticks  `json:"sync_budget_ns"`
	InjectedLoss  int          `json:"injected_losses"`
	Limits        model.Limits `json:"limits"`
}

// DefaultParams returns bounded defaults matching the acceptance criteria.
func DefaultParams() Params {
	return Params{
		ReorderWindow: 64,
		ClockHz:       90000,
		SSRC:          4242,
		SyncBudgetNS:  model.Milli,
		InjectedLoss:  0,
		Limits:        model.DefaultLimits(),
	}
}

// Result is the stable, hashable output summary of one task.
type Result struct {
	TaskKey         string                 `json:"task_key"`
	SyncErrorNS     model.Ticks            `json:"sync_error_ns"`
	P95SyncErrorNS  model.Ticks            `json:"p95_sync_error_ns"`
	LossRecovery    float64                `json:"loss_recovery_rate"`
	P95LatencyMS    float64                `json:"p95_latency_ms"`
	Duplicates      int                    `json:"duplicates"`
	Missing         int                    `json:"missing"`
	Unrecovered     int                    `json:"unrecovered_outside_window"`
	InputSHA256     string                 `json:"input_sha256"`
	IndexSHA256     string                 `json:"index_sha256"`
	ResultSHA256    string                 `json:"result_sha256,omitempty"`
	Committed       int                    `json:"committed"`
	Resumed         bool                   `json:"resumed"`
	Errors          []model.MediaError     `json:"errors,omitempty"`
}

// InterruptError signals a requested stop after N segments; it is a control
// signal, not a processing failure.
var InterruptError = errors.New("interrupt requested")

// Pipeline wires scan, RTP reconstruction, alignment, metrics, checkpointing
// and provenance into one resumable streaming run.
type Pipeline struct {
	dir          string
	params       Params
	paramsDigest string
	taskKey      string
	interruptAt  int // checkpoint count after which to interrupt; <=0 disables
}

// NewPipeline validates parameters and derives the stable task identity.
func NewPipeline(dir string, params Params, inputSHA string) (*Pipeline, error) {
	if err := params.Limits.Validate(); err != nil {
		return nil, err
	}
	pd, err := DigestParams(map[string]any{
		"window": params.ReorderWindow,
		"clock":  params.ClockHz,
		"ssrc":   params.SSRC,
		"budget": int64(params.SyncBudgetNS),
		"loss":   params.InjectedLoss,
		"limit":  params.Limits,
	})
	if err != nil {
		return nil, err
	}
	return &Pipeline{
		dir:          dir,
		params:       params,
		paramsDigest: pd,
		taskKey:      TaskKey(inputSHA, pd),
	}, nil
}

// TaskKey exposes the stable identity.
func (p *Pipeline) TaskKey() string { return p.taskKey }

// SetInterrupt stops the run after checkpointCount checkpoints have been
// written, exercising resume behavior end to end.
func (p *Pipeline) SetInterrupt(checkpointCount int) { p.interruptAt = checkpointCount }

// Run executes (or resumes) the task. mediaPath is the container, eventPath
// the RTP JSONL log; cues come from the subtitle track when present.
func (p *Pipeline) Run(mediaPath, eventPath string, cues []sync.Cue) (*Result, *provenance.Manifest, error) {
	res, indexDigest, err := p.scanWithCache(mediaPath)
	if err != nil {
		return nil, nil, err
	}

	cp, err := LoadCheckpoint(p.dir, p.taskKey)
	if err != nil {
		return nil, nil, err
	}
	resumed := cp != nil
	if cp != nil {
		if gerr := Guard(cp, res.InputSHA256, p.paramsDigest); gerr != nil {
			return nil, nil, gerr
		}
	}

	rec, err := rtp.NewReconstructor(p.params.SSRC, p.params.ReorderWindow, p.params.ClockHz)
	if err != nil {
		return nil, nil, err
	}

	var (
		committedPkts []rtp.Packet
		segments      []Segment
		committed     int
		duplicates    int
		late          int
		seenStart     []uint64
	)
	if cp != nil {
		committed = cp.CommittedCount
		duplicates = cp.Duplicates
		late = cp.Late
		segments = cp.Segments
		seenStart = cp.Seen
		rec.Restore(cp.BaseExt, cp.NextCommitExt, cp.HighestExt, cp.Missing, cp.Seen)
	}

	src, err := rtp.OpenEventLog(eventPath, p.params.Limits)
	if err != nil {
		return nil, nil, err
	}
	defer src.Close()

	checkpointsWritten := 0
	// alreadySeen mirrors the checkpoint's accepted sequences so a resumed
	// run never re-counts packets committed before the interruption.
	alreadySeen := make(map[uint64]struct{}, len(seenStart))
	for _, ext := range seenStart {
		alreadySeen[ext] = struct{}{}
	}
	// seqAnchor maps a 16-bit sequence to its 64-bit extension using the
	// first packet's recorded extension as the deterministic origin.
	var seqAnchor uint64
	hasAnchor := false
	if cp != nil {
		seqAnchor = cp.BaseExt
		hasAnchor = true
	}
	if cp != nil {
		checkpointsWritten = len(cp.Segments)
	}
	var streamErrs []model.MediaError
	for {
		ev, pkt, nerr := src.Next()
		if errors.Is(nerr, io.EOF) {
			break
		}
		if nerr != nil {
			if me := model.AsMediaError(nerr); me != nil {
				streamErrs = append(streamErrs, *me)
				continue // unparseable region: located, then keep processing
			}
			return nil, nil, nerr
		}
		// Replay guard: skip any packet the previous run already accepted.
		if hasAnchor {
			ext := anchorExt(seqAnchor, pkt.SequenceNumber)
			if _, done := alreadySeen[ext]; done {
				continue
			}
		}
		out := rec.Handle(pkt, pktArrival(ev))
		if out.Status == rtp.StatusDuplicate {
			duplicates++
		}
		if out.Status == rtp.StatusLate {
			late++
		}
		if len(out.Committed) == 0 {
			continue
		}
		if len(out.Committed) > 0 {
			committedPkts = append(committedPkts, out.Committed...)
			committed += len(out.Committed)
			segments = append(segments, Segment{
				StartExt: out.Committed[0].ExtSequence,
				EndExt:   out.Committed[len(out.Committed)-1].ExtSequence,
				Count:    len(out.Committed),
			})
			snap := rec.Snapshot(committed)
			snap.Duplicates, snap.Late = duplicates, late
			nextCP := FromStats(p.taskKey, res.InputSHA256, p.paramsDigest, snap,
				rec.NextCommitExt(), rec.HighestExt(), rec.BaseExt(), committed,
				segments, rec.MissingExts(), mergeSeen(seenStart, rec.SeenExts()))
			if err := SaveCheckpoint(p.dir, nextCP); err != nil {
				return nil, nil, err
			}
			checkpointsWritten++
			if p.interruptAt > 0 && checkpointsWritten >= p.interruptAt {
				return nil, nil, InterruptError
			}
		}
	}

	// Final flush: buffered tail and residual gaps.
	flush := rec.Flush()
	if len(flush.Committed) > 0 {
		committedPkts = append(committedPkts, flush.Committed...)
		committed += len(flush.Committed)
		segments = append(segments, Segment{
			StartExt: flush.Committed[0].ExtSequence,
			EndExt:   flush.Committed[len(flush.Committed)-1].ExtSequence,
			Count:    len(flush.Committed),
		})
	}

	latency := metrics.MeasureLatency(committedPkts)
	st := rec.Snapshot(committed)
	st.Duplicates, st.Late = duplicates, late
	loss := metrics.MeasureLoss(st, p.params.InjectedLoss)

	syncRep, syncErr := p.align(res, cues)
	if syncErr != nil {
		return nil, nil, syncErr
	}
	syncStats := metrics.MeasureSync(syncRep, p.params.SyncBudgetNS)

	finalCP := FromStats(p.taskKey, res.InputSHA256, p.paramsDigest, st,
		rec.NextCommitExt(), rec.HighestExt(), rec.BaseExt(), committed,
		segments, rec.MissingExts(), mergeSeen(seenStart, rec.SeenExts()))
	finalCP.Completed = true
	if err := SaveCheckpoint(p.dir, finalCP); err != nil {
		return nil, nil, err
	}

	out := &Result{
		TaskKey:        p.taskKey,
		SyncErrorNS:    syncStats.MaxErrorNS,
		P95SyncErrorNS: syncStats.P95ErrorNS,
		LossRecovery:   loss.RecoveryRate,
		P95LatencyMS:   latency.P95Millis,
		Duplicates:     st.Duplicates,
		Missing:        st.Missing,
		Unrecovered:    loss.Unrecovered,
		InputSHA256:    res.InputSHA256,
		IndexSHA256:    indexDigest,
		Committed:      committed,
		Resumed:        resumed,
		Errors:         streamErrs,
	}
	manifest := provenance.New(p.taskKey, res.InputSHA256, indexDigest, p.paramsDigest)
	manifest.Resumed = resumed
	manifest.CheckpointPath = filepath.Join("checkpoints", p.taskKey+".json")
	for i := range streamErrs {
		manifest.Errors = append(manifest.Errors, streamErrs[i])
	}
	return out, manifest, nil
}

func (p *Pipeline) scanWithCache(mediaPath string) (*index.ScanResult, string, error) {
	indexPath := filepath.Join(p.dir, "indexes", filepath.Base(mediaPath)+".index.json")
	if cached, digest, err := index.LoadIfFresh(indexPath, mediaPath); err != nil {
		return nil, "", err
	} else if cached != nil {
		return cached, digest, nil
	}
	res, err := index.Scan(mediaPath, p.params.Limits)
	if err != nil {
		return res, "", err
	}
	digest, err := index.SaveAtomic(indexPath, res)
	if err != nil {
		return nil, "", err
	}
	return res, digest, nil
}

func (p *Pipeline) align(res *index.ScanResult, cues []sync.Cue) (*sync.Report, error) {
	audio := res.FindTrack("soun")
	if audio == nil {
		return nil, model.AtError(model.ClassMissingTimestamp, res.InputPath, 0, -1,
			"container has no soun track")
	}
	if len(cues) == 0 {
		if sub := res.FindTrack("subt"); sub != nil {
			cues = sync.BuildCuesFromTrack(sub)
		}
	}
	if len(cues) == 0 {
		return &sync.Report{}, nil
	}
	return sync.Align(audio, cues)
}

func pktArrival(ev *rtp.EventRecord) model.Ticks {
	if ev == nil {
		return 0
	}
	return model.Ticks(ev.ArrivalNS)
}

// anchorExt extends a 16-bit sequence relative to the recorded first-packet
// extension, choosing the wrap candidate nearest to the anchor.
func anchorExt(baseExt uint64, seq uint16) uint64 {
	baseSeq := uint16(baseExt % (1 << 16))
	candidate := baseExt - uint64(baseSeq) + uint64(seq)
	delta := int64(seq) - int64(baseSeq)
	switch {
	case delta > (1 << 15):
		candidate -= 1 << 16
	case delta < -(1 << 15):
		candidate += 1 << 16
	}
	return candidate
}

func mergeSeen(existing, fresh []uint64) []uint64 {
	seen := make(map[uint64]struct{}, len(existing)+len(fresh))
	for _, e := range existing {
		seen[e] = struct{}{}
	}
	for _, e := range fresh {
		seen[e] = struct{}{}
	}
	out := make([]uint64, 0, len(seen))
	for e := range seen {
		out = append(out, e)
	}
	sortU64(out)
	return out
}

func sortU64(v []uint64) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j-1] > v[j]; j-- {
			v[j-1], v[j] = v[j], v[j-1]
		}
	}
}
