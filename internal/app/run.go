package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"rtpregress/internal/audio"
	"rtpregress/internal/clock"
	"rtpregress/internal/pipeline"
	"rtpregress/internal/rtp"
	"rtpregress/internal/ts"
)

// Options configures one regression run.
type Options struct {
	InPath      string
	OutDir      string
	Rate        int
	Channels    int
	Bitrate     int
	Workers     int
	Policy      ts.Policy
	CancelAfter int // >0 cancels after this many newly completed segments
	Resume      bool
}

// Exit codes mandated by the command-line contract.
const (
	ExitOK          = 0
	ExitStrictInput = 2
	ExitCanceled    = 75

	// groupDurationNanos coalesces access units into ~2 second work segments.
	groupDurationNanos = 2_000_000_000
)

// RunResult summarizes a run for the CLI layer.
type RunResult struct {
	ExitCode   int
	Canceled   bool
	Checkpoint *Checkpoint
	Stats      ts.Stats
	Manifest   string
	FMp4       string
	Metrics    audio.Metrics
	MaxErrorMS float64
}

// Run executes the full pipeline end to end.
func Run(ctx context.Context, opt Options) (*RunResult, error) {
	if err := os.MkdirAll(opt.OutDir, 0o755); err != nil {
		return nil, clock.New(clock.CodeManifest, "app", -1, 0, 0, "outdir: %v", err)
	}
	inputSHA, err := hashFile(opt.InPath)
	if err != nil {
		return nil, err
	}
	cpPath := CheckpointPath(opt.OutDir)
	var cp *Checkpoint
	if opt.Resume {
		cp, err = LoadCheckpoint(cpPath, inputSHA)
		if err != nil {
			return nil, err
		}
	}
	if cp == nil {
		cp = &Checkpoint{
			Schema:      "rtp-regress/v1",
			InputSHA256: inputSHA,
			WorkerCount: opt.Workers,
			TargetRate:  opt.Rate,
			Channels:    opt.Channels,
			Bitrate:     opt.Bitrate,
			Completed:   []CompletedSegment{},
		}
	}

	// 1. RTP read (truncation-tolerant) + fixed-window reorder.
	in, err := os.Open(opt.InPath)
	if err != nil {
		return nil, clock.New(clock.CodeRTPSync, "app", -1, 0, 0, "open input: %v", err)
	}
	recs, truncOff, err := rtp.ReadAllWithTruncation(ctx, in)
	in.Close()
	if err != nil {
		return nil, err
	}
	rb := rtp.NewReorderBuffer()
	// 2. Reorder RTP, then feed each in-order record's TS payload to the demux
	// while anchoring its 90 kHz media clock so PCR observations can estimate
	// RTP-vs-PCR drift.
	demux := ts.NewDemuxer(nil, opt.Policy)
	var pes []*ts.PESPacket
	var gaps []ts.Gap
	var psi []*ts.PSIEvent
	handleEvent := func(e ts.TimelineEvent) {
		switch {
		case e.PES != nil:
			pes = append(pes, e.PES)
		case e.Gap != nil:
			gaps = append(gaps, *e.Gap)
		case e.PSI != nil:
			psi = append(psi, e.PSI)
		}
	}
	feed := func(rec *rtp.Record) error {
		demux.AnchorRTP(rec.Packet.Timestamp, rec.Offset)
		return demux.ProcessAlignedChunk(rec.Packet.Payload, handleEvent)
	}
	var derr error
	for _, rec := range recs {
		for _, ev := range rb.Push(rec) {
			if ev.Kind == rtp.EventPacket {
				if ferr := feed(ev.Record); ferr != nil {
					derr = ferr
					break
				}
			}
		}
		if derr != nil {
			break
		}
	}
	if derr == nil {
		for _, ev := range rb.Flush() {
			if ev.Kind == rtp.EventPacket {
				if ferr := feed(ev.Record); ferr != nil {
					derr = ferr
					break
				}
			}
		}
	}
	var dstats ts.Stats
	if derr == nil {
		dstats, derr = demux.Finish(handleEvent)
	} else {
		dstats = demux.Stats()
	}
	if derr != nil {
		// strict-mode input fault: stable error, CLI maps to exit 2
		return &RunResult{ExitCode: ExitStrictInput, Stats: dstats}, derr
	}
	if truncOff >= 0 {
		dstats.Truncations++
		var fromNS int64
		if len(pes) > 0 {
			fromNS = pes[len(pes)-1].PTSNanos
		}
		gaps = append(gaps, ts.Gap{
			Offset: truncOff, FromNanos: fromNS, ToNanos: fromNS,
			Code:   clock.CodeTSTruncated,
			Detail: "RTP trace ended in a partial record",
		})
	}

	// 3. Coalesce access units into ~1 s processing segments for stable
	// loudness/bitrate behavior, then place them on the canonical timeline.
	rawSegments := buildSegments(pes)
	segments := audio.GroupSegments(rawSegments, groupDurationNanos)
	planner, err := audio.NewPlanner()
	if err != nil {
		return nil, err
	}
	conv, err := audio.NewConverter(opt.Bitrate)
	if err != nil {
		return nil, err
	}
	pool, err := pipeline.NewPool(opt.Workers, pipeline.MaxQueueJobs, conv)
	if err != nil {
		return nil, err
	}

	// 4. Replay already-completed segments on resume, then process the rest.
	merger := pipeline.NewMerger()
	placementBySeg := map[int]audio.Placement{}
	if err := replayCompleted(cp, planner, merger, placementBySeg); err != nil {
		return nil, err
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan pipeline.Job, pipeline.MaxQueueJobs)
	go func() {
		defer close(jobs)
		for _, seg := range segments {
			if cp.HasSegment(seg.ID) {
				continue
			}
			select {
			case jobs <- pipeline.Job{Segment: seg, PID: seg.PID, SegNumber: seg.ID}:
			case <-workCtx.Done():
				return
			}
		}
	}()

	aacDir := filepath.Join(opt.OutDir, "audio")
	if err := os.MkdirAll(aacDir, 0o755); err != nil {
		return nil, err
	}
	newlyDone := 0
	canceled := false
	perr := pool.Run(workCtx, jobs, func(r pipeline.PoolResult) {
		if r.Err != nil {
			return // converter failure: recorded via metrics, run continues
		}
		pl, perr2 := planner.Place(r.Job.Segment, r.Result.Samples)
		if perr2 != nil {
			return
		}
		placementBySeg[r.Job.SegNumber] = pl
		merger.Add(r, pl)
		row := persistSegment(aacDir, r, pl)
		cp.Completed = append(cp.Completed, row)
		cp.RunHash = ComputeRunHash(cp.Completed)
		_ = cp.Save(cpPath)
		newlyDone++
		if opt.CancelAfter > 0 && newlyDone >= opt.CancelAfter {
			canceled = true
			cancel()
		}
	})
	if canceled || (perr != nil && clock.AsError(perr) != nil &&
		clock.AsError(perr).Code == clock.CodeCanceled) {
		return &RunResult{ExitCode: ExitCanceled, Canceled: true,
			Checkpoint: cp, Stats: dstats}, nil
	}
	if perr != nil {
		return nil, perr
	}

	// 5. Emit the muxed fMP4, validate timing and assemble quality evidence.
	return finalize(opt, cp, planner, merger, gaps, psi, dstats, rb.Stats())
}

// buildSegments maps demuxed PES packets onto audio segments in timeline order.
func buildSegments(pes []*ts.PESPacket) []audio.Segment {
	out := make([]audio.Segment, 0, len(pes))
	for i, p := range pes {
		out = append(out, audio.Segment{
			ID: i, PID: p.PID, StartNanos: p.PTSNanos,
			CodecHint: p.StreamType, Data: p.Data,
		})
	}
	return out
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", clock.New(clock.CodeRTPSync, "app", -1, 0, 0,
			"read input %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func segmentBase(aacDir string, segNum int) string {
	return filepath.Join(aacDir, fmt.Sprintf("seg_%04d", segNum))
}
