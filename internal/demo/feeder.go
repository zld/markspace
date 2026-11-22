package demo

import (
	"context"

	"github.com/medialab/medialab/internal/pipeline"
	"github.com/medialab/medialab/internal/rtprecover"
	"github.com/pion/rtp"
)

// feeder drives one track's packets through a bounded queue into a Recover,
// applying deterministic link faults while all buffering stays bounded by
// the queue capacity and reorder window.
type feeder struct {
	cfg     Config
	fx      *Fixture
	trackID string
	rec     *rtprecover.Recover
	emitted []rtprecover.Sample
	errs    []error
}

func newFeeder(cfg Config, fx *Fixture, trackID string, rec *rtprecover.Recover) *feeder {
	return &feeder{cfg: cfg, fx: fx, trackID: trackID, rec: rec}
}

// produce enqueues source packets in fixture order (already reordered).
// Duplicates are inserted inline; late packets are withheld until enough
// newer packets have been queued to push them outside the reorder window.
func (f *feeder) produce(ctx context.Context, q *pipeline.Queue[*rtp.Packet]) error {
	pkts := f.fx.Packets[f.trackID]
	late := f.fx.LateSeq[f.trackID]
	var heldLate []*rtp.Packet
	for i, pkt := range pkts {
		f.maybeCorrupt(pkt)
		if late != nil && late[pkt.SequenceNumber] {
			// Withhold and re-inject much later, outside the reorder window.
			heldLate = append(heldLate, pkt)
		} else if err := q.Put(ctx, pkt); err != nil {
			return err
		}
		if i == 8 && f.trackID == "v0" {
			// Deterministic duplicate on the video track only: recovery must
			// count and suppress it without emitting twice.
			if err := q.Put(ctx, pkt); err != nil {
				return err
			}
		}
		// Release held late packets after two window-widths of newer traffic
		// have been enqueued, guaranteeing they miss the reorder window.
		if len(heldLate) > 0 && i > 2*f.cfg.WindowCapacity {
			for _, old := range heldLate {
				if err := q.Put(ctx, old); err != nil {
					return err
				}
			}
			heldLate = nil
		}
	}
	for _, old := range heldLate {
		if err := q.Put(ctx, old); err != nil {
			return err
		}
	}
	return nil
}

// stage consumes the queue and hands packets to the recovery window. Push
// rejections (duplicate/late/checksum) are expected fault outcomes captured
// in recovery counters; processing continues regardless.
func (f *feeder) stage(ctx context.Context, q *pipeline.Queue[*rtp.Packet]) error {
	for {
		pkt, ok, err := q.Get(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		// Push rejections (duplicate/late/checksum) are expected fault
		// outcomes captured in recovery counters; processing continues.
		if perr := f.rec.Push(pkt); perr != nil {
			f.errs = append(f.errs, perr)
		}
		samples, derr := f.rec.Drain()
		if derr != nil {
			return derr
		}
		f.emitted = append(f.emitted, samples...)
	}
}

// Events returns the per-packet push errors observed during the run.
func (f *feeder) Events() []error { return f.errs }

// maybeCorrupt flips a payload byte on the deterministic checksum fault.
func (f *feeder) maybeCorrupt(pkt *rtp.Packet) {
	if f.cfg.Fault != FaultChecksum && f.cfg.Fault != FaultComposite {
		return
	}
	if f.trackID != "v0" || pkt.SequenceNumber != 40 {
		return
	}
	if len(pkt.Payload) > 1 {
		pkt.Payload[1] ^= 0xFF
	}
}
