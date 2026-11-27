package pipeline

import (
	"context"

	"github.com/zld/continuity/internal/clock"
	"github.com/zld/continuity/internal/demux"
	"github.com/zld/continuity/internal/index"
	"github.com/zld/continuity/internal/repair"
)

// produce repairs every track and streams its entries into the bounded channel.
// Repair itself is CPU work over an already-parsed table; the channel send is
// the back-pressure point: when the chunker (and behind it the writer) cannot
// keep up, len(entries) reaches ReadQueue and this goroutine blocks, halting
// further reads instead of buffering without limit.
func produce(ctx context.Context, in *demux.File, policy clock.Policy,
	entries chan<- index.Entry, noteQueue func()) error {
	for _, tr := range in.Tracks {
		select {
		case <-ctx.Done():
			return clock.Wrap(clock.StagePipeline, "produce", clock.ErrCanceled)
		default:
		}
		rt, err := repair.One(tr, policy)
		if err != nil {
			// repair errors already carry the repair stage and track context.
			return err
		}
		// The random-access index records seek points only: non-sync samples
		// are reachable by forward decode from the preceding sync entry and
		// must not appear as standalone index rows. Hence index_entries equals
		// the number of sync samples.
		for _, e := range index.FromRepair(rt) {
			if !e.Sync {
				continue
			}
			noteQueue()
			select {
			case entries <- e:
			case <-ctx.Done():
				return clock.Wrap(clock.StagePipeline, "produce", clock.ErrCanceled)
			}
		}
	}
	return nil
}
