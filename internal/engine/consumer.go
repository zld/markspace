package engine

import (
	"context"
	"errors"
	"fmt"

	"mediad/internal/dag"
	"mediad/internal/fixture"
	"mediad/internal/flow"
	"mediad/internal/model"
	"mediad/internal/segment"
	"mediad/internal/ts"
)

// runConsumer drains the bounded packet queue: it drives continuity and PSI
// tracking, the per-representation PCR timelines, the cutters and the muxer.
// Returning errInterrupt means the configured interruption point was hit
// after the in-flight segment was durably committed.
func runConsumer(
	ctx context.Context,
	cfg Config,
	queue *flow.Stream[*model.Packet],
	ct *ts.ContinuityTracker,
	psi *ts.PSITracker,
	routing *routingState,
	reps []*repState,
	stream *fixture.Stream,
	base model.TimeBase,
	align *segment.Align,
	muxer *segment.Muxer,
	cp *dag.Checkpoint,
	recordCorrupt func(*model.CorruptError),
	recordBlocker func(*model.CorruptError),
) error {
	for {
		pkt, ok, err := queue.Recv(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if g := ct.Observe(pkt); g != nil && !g.Repeated {
			recordCorrupt(&model.CorruptError{
				Kind: "continuity", Offset: g.Offset, PID: g.PID,
				Expected: uint16(g.Expected), Actual: uint16(g.Actual),
				Detail: fmt.Sprintf("counter gap expected %d got %d", g.Expected, g.Actual),
			})
			if idx := routing.pcrIndex(g.PID); idx >= 0 {
				reps[idx].timeline.MarkCorruption(g.Offset, g.PID)
			} else if idx := routing.videoIndex(g.PID); idx >= 0 {
				reps[idx].timeline.MarkCorruption(g.Offset, g.PID)
			}
		}
		if ev, err := psi.Observe(pkt); err != nil {
			var ce *model.CorruptError
			if errors.As(err, &ce) {
				recordCorrupt(ce)
			}
		} else if ev != nil {
			// A PAT/PMT version change re-points the PID routing before any
			// subsequent media packet is dispatched.
			routing.refresh(psi)
		}
		// Unmapped media PIDs are paused: record the first blocker rather
		// than emitting packets whose program mapping is unknown. This is a
		// secondary report and must not mask an injected primary defect.
		isPSI := pkt.PID == model.PIDPAT
		for _, p := range stream.PMTPIDs {
			if pkt.PID == p {
				isPSI = true
			}
		}
		if pkt.HasPayload && !isPSI {
			if !psi.IsMapped(pkt.PID) {
				if blocker, blocked := psi.Blocker(pkt.PID); blocked {
					recordBlocker(&model.CorruptError{
						Kind: "psi", Offset: pkt.Offset, PID: pkt.PID,
						Detail: "output paused, first blocker: " + blocker,
					})
				}
				continue
			}
		}
		// PCR packets anchor the representation timeline they belong to.
		if pkt.HasPCR {
			if idx := routing.pcrIndex(pkt.PID); idx >= 0 {
				if _, _, err := reps[idx].timeline.FeedPCR(pkt.PCR, pkt.Offset); err != nil {
					var ce *model.CorruptError
					if errors.As(err, &ce) {
						ce.PID = pkt.PID
						recordCorrupt(ce)
					}
				}
			}
		}
		// Only mapped video PIDs become segments. After routing, the gate
		// compares the segment each representation would open next. The run
		// pauses only once every representation has committed N segments,
		// which keeps the checkpoint symmetric across representations.
		if pkt.HasPayload {
			if idx := routing.videoIndex(pkt.PID); idx >= 0 {
				r := reps[idx]
				if err := routePacket(r, pkt, base, align, muxer, cp); err != nil {
					return err
				}
				if n, armed, err := interruptTarget(cfg.InterruptAt); err != nil {
					return err
				} else if armed && allRepsAtBoundary(reps, n) {
					return errInterrupt
				}
			}
		}
	}
}
