// Package fanout distributes repaired blocks to several bitrate
// representations with independent recovery state. This file applies a
// layer's transport model and byte budget.
package fanout

import (
	"context"
	"time"

	"mediad/internal/media"
	"mediad/internal/merr"
	"mediad/internal/rtp"
)

// admit pushes one tagged block through the reorder window and into the
// budget.
func (l *Layer) admit(ctx context.Context, pb pendingBlock) error {
	l.mu.Lock()
	l.blocks[pb.seq] = pb.block
	ready := l.cursor.Push(rtp.Packet{Sequence: pb.seq, PayloadLen: pb.block.Size()}, nil)
	st := l.cursor.Stats()
	l.stats.Reordered = st.Recovered
	l.stats.Lost = st.Lost
	ordered := make([]media.Block, 0, len(ready))
	for _, p := range ready {
		if blk, ok := l.blocks[p.Sequence]; ok {
			delete(l.blocks, p.Sequence)
			ordered = append(ordered, blk)
		}
	}
	l.mu.Unlock()

	for _, blk := range ordered {
		if err := l.buffer(ctx, blk); err != nil {
			return err
		}
	}
	return nil
}

// buffer applies the byte budget, blocking until space frees.
func (l *Layer) buffer(ctx context.Context, b media.Block) error {
	if b.Size() > l.opts.Buffer {
		return merr.New("fanout.buffer", merr.CodeBadInput, -1,
			"block of %d bytes exceeds the %d-byte layer buffer", b.Size(), l.opts.Buffer)
	}
	start := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for l.bytes+b.Size() > l.opts.Buffer && !l.closed && ctx.Err() == nil {
		l.cond.Wait()
	}
	l.stats.BackpressureNs += time.Since(start).Nanoseconds()
	if l.canceled || l.closed || ctx.Err() != nil {
		return merr.Wrap(errLayerCanceled, "fanout.buffer", merr.CodeLayerCanceled, -1)
	}
	l.queue = append(l.queue, b)
	l.bytes += b.Size()
	if l.bytes > l.stats.PeakBytes {
		l.stats.PeakBytes = l.bytes
	}
	l.cond.Broadcast()
	return nil
}

// Feed offers one block to the layer.
//
// A sequence number is assigned at offer time, before the transport model
// runs, so a swap really presents the receiver with block N+1 before block N.
// Surviving blocks pass through this layer's own RTP reorder window; only
// blocks that emerge in sequence order are admitted to the byte budget, and a
// full budget blocks the publisher rather than dropping data.
func (l *Layer) Feed(ctx context.Context, b media.Block) error {
	if err := ctx.Err(); err != nil {
		return merr.Wrap(err, "fanout.feed", merr.CodeCanceled, -1)
	}

	l.mu.Lock()
	if l.canceled || l.closed {
		l.mu.Unlock()
		return merr.Wrap(errLayerCanceled, "fanout.feed", merr.CodeLayerCanceled, -1)
	}
	l.stats.Offered++
	offer := l.stats.Offered
	seq := uint16(offer)

	if l.opts.Transp.DropEvery > 0 && offer%l.opts.Transp.DropEvery == 0 {
		l.stats.Dropped++
		l.mu.Unlock()
		return nil
	}
	if l.opts.Transp.SwapEvery > 0 && offer%l.opts.Transp.SwapEvery == 0 {
		// Hold this block; the next offer releases it out of order.
		held := pendingBlock{seq: seq, block: b}
		l.delayed = &held
		l.mu.Unlock()
		return nil
	}
	order := []pendingBlock{{seq: seq, block: b}}
	if l.delayed != nil {
		order = append(order, *l.delayed)
		l.delayed = nil
	}
	l.mu.Unlock()

	for _, pb := range order {
		if err := l.admit(ctx, pb); err != nil {
			return err
		}
	}
	return nil
}
