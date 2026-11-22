package buffer

import "mediacore"

// maxQueued bounds packets held between two watermark advances; a stream that
// jumps sequence numbers without ever advancing is rejected rather than
// allocated without limit.
const maxQueued = 1 << 16

// JitterBuffer reorders events by extended sequence number. Events are
// released when their playout deadline (receive time + window) passes.
// Missing sequence positions below the deadline become deterministic gap
// records; packets that arrive after their position was released are
// classified as late and handed back to the caller for timeline replay.
type JitterBuffer struct {
	track        mediacore.TrackID
	window       int64 // nanoseconds
	blockSamples int   // audio samples per block; 0 selects video mode
	channels     int
	rate         int

	queued      map[uint64]mediacore.Event
	initialized bool
	minSeq      uint64 // next sequence position to release
	watermark   int64  // highest observed receive time

	// Late counts packets arriving after their window position was released.
	Late int64
	// Gaps counts deterministic gap records emitted for missing blocks.
	Gaps int64
}

// NewJitterBuffer builds a buffer. For audio pass blockSamples>0 plus channel
// count and sample rate; for video pass blockSamples=0 and no gap records
// are synthesized.
func NewJitterBuffer(track mediacore.TrackID, windowNS int64, blockSamples, channels, rate int) *JitterBuffer {
	return &JitterBuffer{
		track:        track,
		window:       windowNS,
		blockSamples: blockSamples,
		channels:     channels,
		rate:         rate,
		queued:       make(map[uint64]mediacore.Event),
	}
}

// Watermark returns the highest receive time observed so far. The watermark
// only advances on observed events, never on wall-clock assumptions.
func (b *JitterBuffer) Watermark() int64 { return b.watermark }

// Push inserts one parser event. late=true means the packet arrived after its
// sequence position had already been released; it is counted and not stored.
func (b *JitterBuffer) Push(ev mediacore.Event) (late bool, err error) {
	if ev.Recv > b.watermark {
		b.watermark = ev.Recv
	}
	if !b.initialized {
		b.minSeq = ev.Seq
		b.initialized = true
	} else if ev.Seq < b.minSeq {
		b.Late++
		return true, nil
	}
	if _, dup := b.queued[ev.Seq]; dup {
		// Duplicates on still-buffered positions are counted by the parser;
		// the buffer simply refuses to overwrite.
		return false, nil
	}
	if len(b.queued) >= maxQueued {
		return false, mediacore.NewError(mediacore.CatLimit, b.track, "jitter.queue",
			int64(ev.Seq), "jitter queue exceeded maxQueued without watermark advance")
	}
	b.queued[ev.Seq] = ev
	return false, nil
}

// Advance releases every sequence position whose deadline (receive time +
// window) is not later than now, in sequence order. Missing positions become
// gap records in audio mode and are skipped in video mode.
func (b *JitterBuffer) Advance(now int64) []mediacore.Event {
	if now > b.watermark {
		b.watermark = now
	}
	target, ok := b.deadlineTarget(now)
	if !ok {
		return nil
	}
	return b.drain(target)
}

// Flush releases everything still queued at end of stream.
func (b *JitterBuffer) Flush() []mediacore.Event {
	if !b.initialized {
		return nil
	}
	target := b.minSeq
	for seq := range b.queued {
		if seq >= target {
			target = seq
		}
	}
	return b.drain(target)
}

// deadlineTarget returns the highest sequence number whose playout deadline
// has passed at time now.
func (b *JitterBuffer) deadlineTarget(now int64) (uint64, bool) {
	target := uint64(0)
	found := false
	for seq, ev := range b.queued {
		if ev.Recv+b.window <= now && (!found || seq > target) {
			target, found = seq, true
		}
	}
	return target, found
}

// drain emits positions minSeq..target in order, materializing present blocks
// and synthesizing deterministic gaps for absent ones.
func (b *JitterBuffer) drain(target uint64) []mediacore.Event {
	out := make([]mediacore.Event, 0, int(target-b.minSeq)+1)
	for seq := b.minSeq; seq <= target; seq++ {
		if ev, ok := b.queued[seq]; ok {
			delete(b.queued, seq)
			out = append(out, b.materialize(ev))
		} else if b.blockSamples > 0 {
			b.Gaps++
			out = append(out, mediacore.Event{
				Track:    b.track,
				Kind:     mediacore.KindGap,
				Seq:      seq,
				Samples:  b.blockSamples,
				Channels: b.channels,
				Rate:     b.rate,
				Reason:   "missing block past playout window",
			})
		}
		b.minSeq = seq + 1
	}
	return out
}

// materialize converts a reordered parser event into the PCM/video block
// representation consumed by the timeline engine.
func (b *JitterBuffer) materialize(ev mediacore.Event) mediacore.Event {
	if b.blockSamples > 0 {
		ev.Kind = mediacore.KindPCM
		ev.Samples = b.blockSamples
		ev.Channels = b.channels
		ev.Rate = b.rate
	} else {
		ev.Kind = mediacore.KindVideo
	}
	return ev
}
