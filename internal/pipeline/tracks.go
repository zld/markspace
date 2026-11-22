package pipeline

import (
	"sort"

	"example.com/mediaforge/internal/buffer"
	"example.com/mediaforge/internal/clock"
	"example.com/mediaforge/internal/merrors"
	"example.com/mediaforge/internal/recovery"
	"example.com/mediaforge/internal/rtp"
)

// trackResult is the delivery outcome for one stream.
type trackResult struct {
	pkts      []rtp.Packet // delivered media packets, ordered by sequence
	ext       map[uint16]uint64
	recovered map[uint16]bool
	wraps     int
	gaps      int
}

// processTrack runs one stream through ingest, repair, and release.
//
// Phase 1 (ingest) parses each packet as it arrived, extends its RTP timestamp
// onto the stream's monotone timeline, records media in the recovery cache,
// and passes it through the reorderer. Packets outside the reorder window are
// recorded as late and never re-enter; duplicates are recorded and dropped.
//
// Phase 2 (repair) asks the recovery cache to reconstruct each group that lost
// exactly one member. Recovered packets rejoin the ordered stream at their
// real sequence number and carry their real RTP timestamp, so they land in the
// correct place on the timeline.
//
// Phase 3 (release) pushes the ordered stream, arrived and repaired alike,
// through the jitter buffer. Any sequence the window gives up on becomes an
// explicit gap record.
func processTrack(tr TrackInput, in Input, rep *Report) (*trackResult, error) {
	res := &trackResult{
		ext:       make(map[uint16]uint64),
		recovered: make(map[uint16]bool),
	}
	if len(tr.Packets) == 0 {
		return res, nil
	}
	timeline, err := clock.NewTimeline(tr.ClockRate)
	if err != nil {
		return nil, err
	}
	cache, err := recovery.NewCache(tr.SSRC, recovery.Config{
		GroupSize:     in.FECGroupSize,
		MaxGroups:     256,
		MaxGroupBytes: 64 * 1024,
	})
	if err != nil {
		return nil, err
	}
	reorder, err := rtp.NewReorderer(tr.SSRC, rtp.Config{Window: in.ReorderWindow, MaxBuffer: in.MaxBuffer})
	if err != nil {
		return nil, err
	}

	mediaCount := len(tr.Packets)
	groupSize := in.FECGroupSize
	groupMembers := func(k int) []uint16 {
		start := k / groupSize * groupSize
		end := start + groupSize
		if end > mediaCount {
			end = mediaCount
		}
		out := make([]uint16, 0, end-start)
		for i := start; i < end; i++ {
			out = append(out, tr.StartSeq+uint16(i))
		}
		return out
	}

	arrivals := append([]RawPacket(nil), tr.Packets...)
	sort.SliceStable(arrivals, func(i, j int) bool { return arrivals[i].Arrival < arrivals[j].Arrival })

	ordered := make([]rtp.Packet, 0, mediaCount)
	var paritySeqs []uint16
	record := func(err error) {
		switch {
		case merrors.IsCategory(err, merrors.Late):
			rep.Late++
		case merrors.IsCategory(err, merrors.Duplicate):
			rep.Duplicates++
		}
		rep.Record(err)
	}

	// Phase 1: ingest.
	for _, raw := range arrivals {
		pkt, perr := rtp.Parse(raw.Bytes, int64(raw.Arrival))
		if perr != nil {
			rep.Record(perr)
			continue
		}
		// Parity packets are not media: they carry no meaningful RTP
		// timestamp of their own and must never advance the media clock.
		if pkt.PayloadType == ParityPayloadType {
			paritySeqs = append(paritySeqs, pkt.Seq)
			if err := cache.AddParity(tr.SSRC, pkt.Seq, pkt.Payload); err != nil {
				rep.Record(err)
			}
			continue
		}

		ext, terr := timeline.Extend(pkt.Timestamp, raw.Arrival)
		if terr != nil {
			rep.Record(terr)
			continue
		}
		// A duplicate must not overwrite the first arrival's timeline
		// position; its arrival jitter is irrelevant.
		if _, seen := res.ext[pkt.Seq]; !seen || ext < res.ext[pkt.Seq] {
			res.ext[pkt.Seq] = ext
		}
		// Video media arrives framed for FEC; the cache stores frames so
		// parity members are equal length. The delivered payload is the
		// unframed media, recovered below.
		payload := pkt.Payload
		framed := true
		if t, p, uerr := Unframe(pkt.Payload); uerr == nil {
			_ = t
			payload = p
		} else {
			framed = false
		}
		if framed {
			if err := cache.AddMedia(tr.SSRC, pkt.Seq, pkt.Payload); err != nil {
				rep.Record(err)
			}
		}
		pkt.Payload = payload
		out, gaps, err := reorder.Push(pkt)
		if err != nil {
			record(err)
			continue
		}
		ordered = append(ordered, out...)
		// Reorder-window gaps are recorded as diagnostics only. FEC repair
		// runs after ingest and can refill them, and the jitter buffer reports
		// the final holes in the delivered sequence; counting here too would
		// double-report the same loss.
		for _, g := range gaps {
			rep.Record(merrors.Packet(merrors.StageRTPReorder, merrors.Gap, tr.SSRC, g.From,
				"reorder window passed sequences from %d (%d packets)", g.From, g.Count))
		}
	}
	tail, tgaps := reorder.Flush()
	ordered = append(ordered, tail...)
	for _, g := range tgaps {
		rep.Record(merrors.Packet(merrors.StageRTPReorder, merrors.Gap, tr.SSRC, g.From,
			"trailing reorder gap from %d (%d packets)", g.From, g.Count))
	}

	// Phase 2: repair.
	present := make(map[uint16]rtp.Packet, len(ordered))
	for _, p := range ordered {
		present[p.Seq] = p
	}
	for k := 0; k < mediaCount; k += groupSize {
		seq := tr.StartSeq + uint16(k)
		result, rerr := cache.Repair(seq, groupMembers(k))
		if rerr != nil {
			// An unrecoverable group's missing sequences will also surface
			// as a jitter-buffer gap, which is the authoritative count. Here
			// the failure is recorded as its own diagnostic without
			// incrementing the gap total.
			if merrors.IsCategory(rerr, merrors.Unrecoverable) || merrors.IsCategory(rerr, merrors.Checksum) {
				rep.Record(rerr)
			} else if !merrors.IsCategory(rerr, merrors.Empty) {
				rep.Record(rerr)
			}
			cache.Retire(seq)
			continue
		}
		for _, rseq := range result.Recovered {
			if _, already := present[rseq]; already {
				// It arrived beyond the reorder window, or after the
				// reorderer had already released past it; the arrival wins.
				continue
			}
			frame, ok := cache.Rebuilt(rseq)
			if !ok {
				continue
			}
			ts, payload, uerr := Unframe(frame)
			if uerr != nil {
				rep.Record(uerr)
				continue
			}
			pkt := rtp.Packet{
				SSRC: tr.SSRC, Seq: rseq, Timestamp: ts,
				PayloadType: 96, Payload: payload,
			}
			// A repaired packet was never seen, so it has no recorded
			// timeline position. Re-extending its raw timestamp against the
			// stream high-water mark would misplace it by a whole epoch once
			// a wrap has passed; placeRepaired instead anchors it to a
			// neighbour's extended position.
			if _, have := res.ext[rseq]; !have {
				ext, xerr := placeRepaired(present, rseq, ts, res.ext)
				if xerr != nil {
					rep.Record(xerr)
					continue
				}
				res.ext[rseq] = ext
			}
			present[rseq] = pkt
			res.recovered[rseq] = true
			rep.Recovered++
		}
		cache.Retire(seq)
	}
	_ = paritySeqs

	// Phase 3: release in sequence order through the jitter buffer.
	all := make([]rtp.Packet, 0, len(present))
	for _, p := range present {
		all = append(all, p)
	}
	sort.SliceStable(all, func(i, j int) bool {
		return rtp.SeqDelta(all[i].Seq, all[j].Seq) < 0
	})

	jitter, err := buffer.New(tr.SSRC, buffer.Config{
		WindowTicks: in.JitterWindowTicks,
		MaxUnits:    in.MaxBuffer,
		HighWater:   in.MaxBuffer,
	})
	if err != nil {
		return nil, err
	}
	emit := func(units []buffer.Unit, gaps []buffer.Gap) {
		for _, u := range units {
			res.pkts = append(res.pkts, rtp.Packet{
				SSRC: u.SSRC, Seq: u.Seq, Timestamp: u.Timestamp,
				Marker: u.Marker, Payload: u.Payload,
			})
			res.ext[u.Seq] = u.Extended
		}
		for _, g := range gaps {
			res.gaps++
			rep.Record(merrors.Packet(merrors.StageJitter, merrors.Gap, tr.SSRC, g.FromSeq,
				"jitter gap of %d from sequence %d (after tick %d)", g.Count, g.FromSeq, g.AfterTick))
		}
	}
	for _, p := range all {
		u := buffer.Unit{
			SSRC: p.SSRC, Seq: p.Seq, Timestamp: p.Timestamp,
			Extended: res.ext[p.Seq], Marker: p.Marker,
			Payload: p.Payload, Arrival: uint64(p.ArrivalNanos),
		}
		if err := jitter.Push(u); err != nil {
			if merrors.IsCategory(err, merrors.Duplicate) || merrors.IsCategory(err, merrors.Late) {
				record(err)
				continue
			}
			return nil, err
		}
		units, gaps := jitter.Ready()
		emit(units, gaps)
	}
	units, gaps := jitter.Flush()
	emit(units, gaps)

	res.wraps = timeline.Wraps()
	return res, nil
}

// placeRepaired puts a recovered packet on the timeline.
//
// The parity frame preserves the lost packet's own 32-bit RTP timestamp, so
// its position is the neighbour's extended position plus the signed 32-bit
// delta between the two raw timestamps. Neighbours always lie within the same
// 2^31-tick half-space, so the delta is unambiguous even across a wrap.
// Extending the raw timestamp against the stream's current high-water mark
// instead would misplace it by a whole epoch after a wrap.
func placeRepaired(present map[uint16]rtp.Packet, seq uint16, raw uint32, ext map[uint16]uint64) (uint64, error) {
	// Choose the placed neighbour nearest in sequence order, preferring an
	// earlier packet so ties resolve toward the packet the loss followed.
	bestDist := 0
	var best uint16
	found := false
	for cand := range present {
		ref, ok := ext[cand]
		if !ok || ref == 0 {
			continue
		}
		d := rtp.SeqDelta(cand, seq)
		if d == 0 {
			continue
		}
		dist := d
		if dist < 0 {
			dist = -dist
		}
		if !found || dist < bestDist || (dist == bestDist && d < 0) {
			best, bestDist, found = cand, dist, true
		}
	}
	if !found {
		return 0, merrors.Packet(merrors.StagePipeline, merrors.Empty, 0, seq,
			"repaired packet has no placed neighbour")
	}
	// Sign extension is only unambiguous within +-2^31 ticks, i.e. for any
	// neighbour within half the 16-bit sequence space, which "nearest" is.
	if bestDist > 0x4000 {
		return 0, merrors.Packet(merrors.StagePipeline, merrors.OutOfRange, 0, seq,
			"repaired packet's nearest neighbour is %d sequences away", bestDist)
	}
	return clock.SignExtend(raw, ext[best]), nil
}
