package session

import (
	"context"
	"time"

	"avcheck/internal/ingest"
	"avcheck/internal/media"
	"avcheck/internal/pipeline"
	"avcheck/internal/render"
	"avcheck/internal/timeline"
	"avcheck/internal/transport"
)

// producePackets encodes each wire frame as one RTP packet, injects the
// configured faults and pushes arrival-ordered packets into the queue. Tracks
// are interleaved so backpressure blocks a realistic multiplex.
func producePackets(ctx context.Context, q *pipeline.Queue[wirePacket], gen ingest.GenConfig,
	cfg Config, faults Faults, stats []TrackResult) error {
	fcfg := DefaultFaultConfig()
	injs := make([]*injector, cfg.Tracks)
	for i := range injs {
		injs[i] = newInjector(cfg.Seed+int64(i)*7919, faults, fcfg)
	}
	for index := 0; index < cfg.Frames; index++ {
		for ti := 0; ti < cfg.Tracks; ti++ {
			f, err := ingest.WireFrame(gen, ti, index)
			if err != nil {
				return err
			}
			meta := transport.PacketMeta{
				StreamID: f.StreamID, FrameSeq: f.Seq, Ticks: f.Ticks, Key: f.Key,
				MetaVersion: f.Meta.Version, Length: f.Meta.Length, Sum: f.Sum,
			}
			raw, err := transport.EncodePacket(meta, f.Payload, uint16(index), uint32(f.Ticks), f.StreamID)
			if err != nil {
				return err
			}
			for _, p := range injs[ti].push(raw, index) {
				if err := q.Send(ctx, wirePacket{track: ti, raw: p.raw, at: p.at}); err != nil {
					return err
				}
			}
		}
	}
	for ti := range injs {
		for _, p := range injs[ti].flush() {
			if err := q.Send(ctx, wirePacket{track: ti, raw: p.raw, at: p.at}); err != nil {
				return err
			}
		}
		// Received counts unique capture frames handed to the network; the
		// identity accepted+rejected+lost == received holds per track.
		stats[ti].Received = uint64(cfg.Frames)
		stats[ti].Duplicates = uint64(injs[ti].Counts.Duplicated)
	}
	return nil
}

// decodeValidate parses packets, repairs ordering, validates frames and
// offers accepted frames to the composer. Every accepted frame is also
// appended to its track's streaming MP4 index.
func decodeValidate(ctx context.Context, in *pipeline.Queue[wirePacket],
	out *pipeline.Queue[recoveredFrame], cfg Config, stats []TrackResult,
	builders []*ingest.MP4Builder) error {
	reorders := make([]*transport.Reorderer, cfg.Tracks)
	validators := make([]*ingest.Validator, cfg.Tracks)
	estimators := make([]*transport.Estimator, cfg.Tracks)
	for i := range reorders {
		reorders[i] = transport.NewReorderer(uint32(i+1), cfg.ReorderWindow, cfg.BurstThresh)
		validators[i] = ingest.NewValidator(cfg.Policy)
		// The estimator only retunes the reorder window; queue capacity stays
		// fixed, so backpressure is unaffected by rate shifts.
		minW := cfg.ReorderWindow
		if minW > 8 {
			minW = 8
		}
		estimators[i] = transport.NewEstimator(minW, cfg.ReorderWindow, 50*time.Millisecond)
	}
	epoch := time.Unix(0, 0)
	// A packet damaged beyond RTP parsing cannot be attributed to a frame;
	// count it against the track that carried it and skip it.
	reject := func(ti int, err error) {
		st := &stats[ti]
		st.Rejected++
		if me, ok := err.(*media.Error); ok && me.Kind == media.KindTruncated {
			st.Truncated++
		} else {
			st.Corrupt++
		}
	}
	accept := func(ti int, f *media.Frame) error {
		if stats[ti].State == timeline.TrackFailed {
			stats[ti].Rejected++
			return nil
		}
		if err := builders[ti].Add(f); err != nil {
			return err
		}
		return out.Send(ctx, recoveredFrame{track: ti, frame: f})
	}
	for {
		wp, ok := in.Recv(ctx)
		if !ok {
			break
		}
		pkt, _, err := transport.DecodePacket(wp.raw)
		if err != nil {
			reject(wp.track, err)
			continue
		}
		r := reorders[wp.track]
		r.Window = estimators[wp.track].WindowHint()
		estimators[wp.track].Observe(len(wp.raw), epoch.Add(time.Duration(wp.at)))
		r.Window = estimators[wp.track].WindowHint()
		outs, recovered, gaps := r.PushEx(pkt)
		st := &stats[wp.track]
		for _, g := range gaps {
			st.GapEvents++
			st.GapPackets += uint64(g.Count)
			if g.Burst {
				st.BurstGaps++
			}
		}
		if len(recovered) > 0 || len(gaps) > 0 {
			if stats[wp.track].State == timeline.TrackClean {
				stats[wp.track].State = timeline.TrackDegraded
			}
		}
		for _, op := range outs {
			om, merr := transport.MetaOf(op)
			if merr != nil {
				reject(wp.track, merr)
				continue
			}
			f := om.Frame(op.Payload)
			if len(recovered) > 0 {
				f.State = media.StateRecovered
			}
			if err := validators[wp.track].Check(f); err != nil {
				st.Rejected++
				if me, ok := err.(*media.Error); ok {
					switch me.Kind {
					case media.KindTruncated:
						st.Truncated++
					case media.KindChecksum, media.KindCorrupt:
						st.Corrupt++
					}
				}
				if cfg.Policy == ingest.PolicyFailTrack {
					st.State = timeline.TrackFailed
					st.Failure = err
				}
				continue
			}
			st.Accepted++
			if st.GapPackets > 0 && f.State == media.StateClean {
				f.State = media.StateDegraded
			}
			if err := accept(wp.track, f); err != nil {
				return err
			}
		}
	}
	for ti := range reorders {
		outs, gaps := reorders[ti].Flush()
		for _, op := range outs {
			om, merr := transport.MetaOf(op)
			if merr != nil {
				stats[ti].Rejected++
				continue
			}
			f := om.Frame(op.Payload)
			if err := validators[ti].Check(f); err != nil {
				stats[ti].Rejected++
				continue
			}
			stats[ti].Accepted++
			if err := accept(ti, f); err != nil {
				return err
			}
		}
		if len(gaps) > 0 {
			stats[ti].GapEvents += uint64(len(gaps))
			for _, g := range gaps {
				stats[ti].GapPackets += uint64(g.Count)
				if g.Burst {
					stats[ti].BurstGaps++
				}
			}
		}
		stats[ti].Late = reorders[ti].Stats.Late
		stats[ti].Duplicates = reorders[ti].Stats.Duplicates
		stats[ti].Reordered = reorders[ti].Stats.Reordered
	}
	return nil
}

// composeExport drains validated frames into the streaming composer and, for
// every decided slot, writes one PNG and manifest row.
func composeExport(ctx context.Context, in *pipeline.Queue[recoveredFrame],
	sc *timeline.StreamComposer, writer *render.Writer) error {
	emit := func(of timeline.OutputFrame) error { return writer.Add(of) }
	for {
		fr, ok := in.Recv(ctx)
		if !ok {
			break
		}
		if err := sc.Offer(fr.frame.StreamID, fr.frame); err != nil {
			return err
		}
		if err := sc.Drain(emit); err != nil {
			return err
		}
	}
	// A canceled run must stop promptly: flushing would render placeholder
	// frames for the entire unread remainder of the timeline.
	if err := ctx.Err(); err != nil {
		return media.NewError(media.KindCanceled, 0, sc.NextIndex(), -1, "export canceled: %v", err)
	}
	// Every track is finalized at once: the frame queue closes only after all
	// of them have been drained, so Flush can decide the tail slots.
	return sc.Flush(emit)
}
