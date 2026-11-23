package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/zld/media-check/media"
	"github.com/zld/media-check/transport"
)

// FusionOptions wires the receive subsystem into a pipeline run.
type FusionOptions struct {
	// RTP is the receive drill to replay before segment processing. Its
	// finalized gap records and counters are folded into the manifest.
	RTP *transport.RTPDrill
	// GapWait is how long the producer waits for the transport gap report to
	// settle before it starts dispatching segment jobs.
	GapWait time.Duration
	// MaxGaps bounds how many gap records are accepted before the run is
	// declared degraded and no artifacts are committed.
	MaxGaps int
	// Bandwidth supplies the receive-side controller. When set, its final
	// concurrency and queue limits drive the worker pool.
	Bandwidth *transport.Controller
}

// RunFused runs the media pipeline with a live transport report. It is the
// production-consumption chain described in the README: the RTP subsystem
// parses and reorders packets and publishes ordered packets plus gap records,
// the producer waits for that report, then the bounded pool dispatches
// segments, and the merger emits a deterministic manifest.
func RunFused(ctx context.Context, opts RunOptions, fuse FusionOptions) (*RunReport, error) {
	if fuse.MaxGaps <= 0 {
		fuse.MaxGaps = 64
	}
	var rtpReport *RTPReport
	if fuse.RTP != nil {
		stats, gaps, ordered, err := fuse.RTP.Replay(4, 64, 0x4d454449)
		if err != nil {
			return nil, err
		}
		// The receive side must hand the pipeline a strictly increasing stream;
		// otherwise no ordering claim downstream is meaningful.
		for i := 1; i < len(ordered); i++ {
			if ordered[i].ExtSeq <= ordered[i-1].ExtSeq {
				return nil, media.NewError(media.ClassIntegrity, "fusion.order", 0,
					"transport released packet %d after %d", ordered[i].ExtSeq, ordered[i-1].ExtSeq)
			}
		}
		if len(gaps) > fuse.MaxGaps {
			return nil, media.NewError(media.ClassGap, "fusion.gaps", 0,
				"%d gap records exceed limit %d", len(gaps), fuse.MaxGaps)
		}
		recs := make([]GapRecord, 0, len(gaps))
		for _, g := range gaps {
			recs = append(recs, GapRecord{Start: g.Start, End: g.End, State: string(g.State)})
		}
		rtpReport = &RTPReport{
			Received: stats.Received, Duplicates: stats.Duplicates,
			Reordered: stats.Reordered, LateArrived: stats.LateArrived,
			Lost: stats.Lost, Recovered: stats.Recovered,
			Gaps: recs, LossFree: stats.Lost == 0,
		}
		if fuse.Bandwidth != nil {
			minBps, meanBps, maxBps := fuse.Bandwidth.Summary()
			rtpReport.MinBps, rtpReport.MeanBps, rtpReport.MaxBps = minBps, meanBps, maxBps
			// The controller's effective limits bound the pool. Its floor is
			// 1, so take the operator's concurrency as the base and let the
			// controller's ratio against its own maximum scale it down.
			pl, q := fuse.Bandwidth.Limits()
			rtpReport.FinalParallel, rtpReport.FinalQueue = pl, q
			if opts.Workers <= 0 || pl < opts.Workers {
				opts.Workers = pl
			}
			if opts.Queue <= 0 || q < opts.Queue {
				opts.Queue = q
			}
		}
	}
	rep, err := runMedia(ctx, opts, rtpReport)
	if err != nil {
		return nil, err
	}
	return rep, nil
}

// runMedia is Run with the transport report already computed, so that the
// manifest digest covers a single, complete evidence set.
func runMedia(ctx context.Context, opts RunOptions, rtp *RTPReport) (*RunReport, error) {
	rep, err := Run(ctx, opts)
	if err != nil {
		return nil, err
	}
	if rtp != nil {
		rep.Map.RTP = rtp
		// RTP evidence changes the digest, so recompute and rewrite atomically.
		sha, err := rep.Map.Digest()
		if err != nil {
			return nil, err
		}
		rep.Map.SHA256 = sha
		if err := writeJSONAtomic(filepath.Dir(rep.ManifestPath), rep.ManifestPath, rep.Map); err != nil {
			return nil, err
		}
	}
	return rep, nil
}

func (r *RunReport) String() string {
	return fmt.Sprintf("run manifest=%s tracks=%d segments=%d complete=%t",
		r.Map.SHA256[:12], r.Map.Tracks, r.Map.Segments, r.Map.Complete)
}
