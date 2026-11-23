package pipeline

import (
	"sort"

	"mediadist/internal/demux"
	"mediadist/internal/merr"
	"mediadist/internal/timeline"
	"mediadist/internal/transport"
)

// prepareTransport parses, deduplicates, reorders and records loss.
func prepareTransport(in *Input, opts Options) (
	[]demux.StreamSpec, map[int][]*transport.Packet, map[int][]transport.SeqSpan,
	[]StreamReport, []*merr.Error, error) {
	var specs []demux.StreamSpec
	packets := make(map[int][]*transport.Packet, len(in.Streams))
	gaps := make(map[int][]transport.SeqSpan, len(in.Streams))
	stats := make([]StreamReport, 0, len(in.Streams))
	var allErrs []*merr.Error
	segmentNS := int64(opts.SegmentMS) * 1e6

	for si, src := range in.Streams {
		lim := transport.DefaultLimits()
		parsed, err := transport.ParseStream(si, src.Datagrams, lim, opts.StrictTransport)
		if err != nil {
			return nil, nil, nil, nil, nil, err
		}
		allErrs = append(allErrs, parsed.Errors...)
		buf, err := transport.NewReorderBuffer(transport.DefaultReorderConfig(si), lim)
		if err != nil {
			return nil, nil, nil, nil, nil, err
		}
		rec := transport.NewLossRecorder(si, 1024)

		var committed []*transport.Packet
		// Committed gaps are authoritative loss: the reorder buffer already
		// decided no packet will fill them. Pending gaps are repairable and
		// stay out of the lost evidence until they commit.
		commit := func(out []*transport.Packet) {
			if len(out) == 0 {
				return
			}
			committed = append(committed, out...)
			recordCommittedGaps(buf, rec)
		}
		// Retransmissions are delivered as soon as the reorder buffer has
		// declared the hole but before it commits past it. The window bounds
		// how far ahead a repair may be held.
		repairSeqs := make([]int, 0, len(src.Repairs))
		for seq := range src.Repairs {
			repairSeqs = append(repairSeqs, int(seq))
		}
		sort.Ints(repairSeqs)
		// deliverRepairs offers each still-held retransmission whose hole the
		// buffer has declared but not yet committed. Offering after a push is
		// essential: a packet's arrival is what reveals the hole below it.
		deliverRepairs := func() {
			remain := repairSeqs[:0]
			for _, seq := range repairSeqs {
				if !buf.PendingHole(uint16(seq)) {
					remain = append(remain, seq)
					continue
				}
				rp, perr := transport.Parse(src.Repairs[uint16(seq)], transport.ParseOptions{Stream: si})
				if perr != nil {
					continue
				}
				if !rec.Recover(int64(rp.Seq)) {
					remain = append(remain, seq)
					continue
				}
				commit(buf.PushRecovered(rp))
			}
			repairSeqs = remain
		}
		notePending := func() {
			for _, g := range buf.PendingGaps() {
				rec.RecordPending(g.First, g.Last)
			}
		}
		for _, p := range parsed.Packets {
			notePending()
			commit(buf.Push(p))
			notePending()
			deliverRepairs()
		}
		// Holes still pending at end of stream get one last repair attempt.
		notePending()
		deliverRepairs()
		committed = append(committed, buf.Flush()...)
		recordCommittedGaps(buf, rec)

		// Late arrivals are evidence outside committed fragments; record them
		// so the loss report shows exactly which sequences could not be used.
		for _, seq := range buf.LateArrivals() {
			rec.RecordLateSeq(seq)
		}
		lost, recovered := rec.Counts()
		st := buf.Stats()
		stats = append(stats, StreamReport{
			Name: src.Name, Stream: si, TrackID: src.TrackID, Codec: src.Codec,
			Timescale: src.Timescale,
			Received:  st.Received, Accepted: st.Accepted, Committed: st.Committed,
			Duplicates: st.Duplicate, Reordered: st.Reordered, Late: st.Late,
			LostSequences: lost, RecoveredSeqs: recovered,
			LostBursts: rec.LostBursts(), RecoveredBursts: rec.RecoveredBursts(),
		})
		allErrs = append(allErrs, buf.Errors()...)
		allErrs = append(allErrs, rec.Errors()...)

		// The segment grid is relative to each stream's declared origin, so a
		// non-zero RTP start does not push every sample into a huge segment
		// index. The origin itself remains visible in the stream report.
		// Expanded RTP timestamps for this stream. Using the first packet's raw
		// timestamp as the clock origin keeps later expansion on the short
		// path; a declared non-zero origin is added on top.
		originTicks := rawOrigin(parsed.Packets)
		clock, cerr := timeline.NewClock(src.Timescale, 32)
		if cerr != nil {
			return nil, nil, nil, nil, nil, cerr
		}
		// Demux primes its own clock on the first packet, so the stream's
		// declared origin is recorded here and used to normalize the segment
		// grid without altering any media timestamp.
		gridOriginNS := clock.ToNS(originTicks)
		stats[len(stats)-1].OriginTicks = originTicks
		stats[len(stats)-1].OriginNS = gridOriginNS
		// The unified segment grid starts at each stream's first sample, so a
		// large non-zero RTP origin does not scatter samples across millions
		// of empty segments. The origin stays visible in the stream report.
		specs = append(specs, demux.StreamSpec{
			Stream: si, TrackID: src.TrackID, Codec: demux.Codec(src.Codec),
			Timescale: src.Timescale, WrapBits: 32, FrameDuration: 3000,
			SegmentNS: segmentNS, OriginNS: gridOriginNS,
			ClockStart: 0,
		})
		packets[si] = committed
		// Only committed loss counts as missing media. Recovered intervals are
		// present by the time the sample is reassembled.
		var spans []transport.SeqSpan
		for _, iv := range rec.Intervals() {
			if iv.State != transport.LossLost {
				continue
			}
			spans = append(spans, transport.SeqSpan{Stream: si, First: iv.First, Last: iv.Last})
		}
		gaps[si] = spans
	}
	return specs, packets, gaps, stats, allErrs, nil
}

// rawOrigin returns the first packet's expanded timestamp, used to anchor a
// stream clock without forcing a wrap jump on the second sample.
func rawOrigin(packets []*transport.Packet) int64 {
	if len(packets) == 0 {
		return 0
	}
	return int64(packets[0].Timestamp)
}

// recordCommittedGaps forwards this pass's committed holes to the loss
// recorder, merging adjacent ones so one contiguous burst is reported as one
// interval rather than one interval per released packet.
func recordCommittedGaps(buf *transport.ReorderBuffer, rec *transport.LossRecorder) {
	gaps := buf.CommittedGaps()
	if len(gaps) == 0 {
		return
	}
	first, last := gaps[0].First, gaps[0].Last
	for _, g := range gaps[1:] {
		if g.First == last+1 {
			last = g.Last
			continue
		}
		rec.RecordLost(first, last)
		first, last = g.First, g.Last
	}
	rec.RecordLost(first, last)
}
