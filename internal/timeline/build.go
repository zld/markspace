// Package timeline turns reconstructed RTP records and codec duration parsing
// into one explicit, integer-nanosecond timeline per track.
//
// Rules enforced here:
//   - duplicates are recorded but add zero duration and zero accounted bytes;
//   - missing sequence numbers keep a gap entry so the loss is visible, and no
//     audio bytes are fabricated for it;
//   - a packet whose duration cannot be determined records a precise failure
//     (stage + packet offset + reason) instead of being skipped;
//   - every entry carries the time/byte delta against the lossless ordered
//     stream, which is exactly zero on a clean input.
package timeline

import (
	"sort"

	"mediasync/internal/audio"
	"mediasync/internal/model"
	"mediasync/internal/rtp"
)

// Builder holds the per-codec duration functions.
type Builder struct{}

// New returns a ready builder.
func New() *Builder { return &Builder{} }

// Build constructs the timeline of one track from its RTP reconstruction.
// mp4Info, when non-nil (AAC tracks with an MP4 sample table), supplies
// authoritative per-sample durations; RTP AU headers are used otherwise.
func (b *Builder) Build(meta model.TrackMeta, rec *rtp.Result, mp4Info *audio.MP4Info) *model.TrackTimeline {
	tl := &model.TrackTimeline{
		TrackID:     meta.TrackID,
		FirstExtSeq: rec.FirstExtSeq,
		LastExtSeq:  rec.LastExtSeq,
	}

	// Gap lookup: present sequence numbers map to their first record.
	present := make(map[uint64]bool, len(rec.Records))
	firstRecord := make(map[uint64]int, len(rec.Records))
	for i, r := range rec.Records {
		present[r.ExtSeq] = true
		if _, ok := firstRecord[r.ExtSeq]; !ok {
			firstRecord[r.ExtSeq] = i
		}
	}

	var (
		cursorNS      model.NS // placed end of the accounted stream
		idealNS       model.NS // end a lossless stream would have reached
		idealBytes    int      // bytes a lossless stream would have accounted
		actualBytes   int      // bytes actually accounted (duplicates excluded)
		gapFillNS     model.NS // last known real frame duration, used to space gaps
		mp4SampleIdx  int
	)

	emit := func(seq uint64) {
		if !present[seq] {
			// Missing number: preserve the slot, fabricate no audio.
			entry := model.TimelineEntry{
				Class:       model.EntryGap,
				SourceIndex: -1,
				ExtSeq:      seq,
				Start:       cursorNS,
				Duration:    gapFillNS,
				TimeDelta:   cursorNS - idealNS,
				Bytes:       0,
				ByteDelta:   actualBytes - idealBytes,
			}
			cursorNS += gapFillNS
			tl.Entries = append(tl.Entries, entry)
			return
		}

		idx := firstRecord[seq]
		// Every received copy of this sequence: first one timed, the rest are
		// zero-duration duplicates. Records are already (ext, offset) sorted.
		for j := idx; j < len(rec.Records) && rec.Records[j].ExtSeq == seq; j++ {
			r := rec.Records[j]
			if r.Duplicate {
				tl.Entries = append(tl.Entries, model.TimelineEntry{
					Class:       model.EntryDuplicate,
					SourceIndex: r.Offset,
					ExtSeq:      seq,
					Start:       cursorNS,
					Duration:    0,
					TimeDelta:   cursorNS - idealNS,
					Bytes:       0,
					ByteDelta:   actualBytes - idealBytes,
				})
				continue
			}
			dur, nbytes, fail := b.duration(meta, r, mp4Info, &mp4SampleIdx)
			entry := model.TimelineEntry{
				Class:       model.EntrySample,
				SourceIndex: r.Offset,
				ExtSeq:      seq,
				Start:       cursorNS,
				Duration:    dur,
				TimeDelta:   cursorNS - idealNS,
				Bytes:       nbytes,
				ByteDelta:   actualBytes - idealBytes,
			}
			if fail != nil {
				fail.TrackID = meta.TrackID
				entry.Failure = fail
				tl.Failures = append(tl.Failures, *fail)
			} else {
				if dur > gapFillNS {
					gapFillNS = dur
				}
			}
			tl.Entries = append(tl.Entries, entry)

			// Real sample advances both placed and ideal accounting.
			cursorNS += dur
			actualBytes += nbytes
			idealNS += dur
			idealBytes += nbytes
		}
	}

	// Walk the inclusive extended range so gaps and samples interleave in order.
	for seq := rec.FirstExtSeq; seq <= rec.LastExtSeq; seq++ {
		emit(seq)
	}

	// Pull in parse-level failures not already attached as entry failures.
	attachParseFailures(tl, rec)
	sortEntries(tl.Entries)
	return tl
}

// duration resolves one non-duplicate record's timing and payload size.
func (b *Builder) duration(meta model.TrackMeta, r rtp.Record,
	mp4Info *audio.MP4Info, mp4Idx *int) (model.NS, int, *model.Failure) {
	if r.ParseError != nil {
		f := *r.ParseError
		return 0, 0, &f
	}
	switch meta.Codec {
	case model.CodecOpus:
		p, fail := audio.PacketDuration(r.Payload, r.Offset)
		if fail != nil {
			return 0, 0, fail
		}
		return p.DurationNS, len(r.Payload), nil
	case model.CodecAAC:
		if mp4Info != nil && *mp4Idx < len(mp4Info.Samples) {
			s := mp4Info.Samples[*mp4Idx]
			*mp4Idx++
			return s.DeltaNS, int(s.Size), nil
		}
		aus, fail := audio.ParseAUHeaders(r.Payload, r.Offset)
		if fail != nil {
			return 0, 0, fail
		}
		// Without an MP4 table the AU frame duration is derived from the
		// track sample rate (1024 samples/AAC frame), exact integer math.
		var total model.NS
		var nbytes int
		for _, au := range aus {
			ns, f := audio.SampleDurationNS(1024, meta.SampleRate, r.Offset)
			if f != nil {
				return 0, 0, f
			}
			total += ns
			nbytes += au.Size
		}
		return total, nbytes, nil
	default:
		f := model.PtrFailure(meta.TrackID, model.StageTimeline, r.Offset,
			model.ErrRTPParse, "unsupported codec "+string(meta.Codec))
		return 0, 0, f
	}
}

// attachParseFailures ensures every RTP-level failure appears in the audit
// trail exactly once, even when no timeline entry could host it.
func attachParseFailures(tl *model.TrackTimeline, rec *rtp.Result) {
	have := make(map[int]bool)
	for _, e := range tl.Entries {
		if e.Failure != nil {
			have[e.Failure.PacketOffset] = true
		}
	}
	for i := range rec.Failures {
		f := rec.Failures[i]
		if f.Code == model.ErrRTPParse && have[f.PacketOffset] {
			continue // duplicate-sequence failures already represented
		}
		if !have[f.PacketOffset] {
			tl.Failures = append(tl.Failures, f)
		}
	}
}

// sortEntries gives a canonical order: start time, then extended sequence,
// then source index. Same-timestamp events are therefore stable across runs.
func sortEntries(entries []model.TimelineEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		if a.ExtSeq != b.ExtSeq {
			return a.ExtSeq < b.ExtSeq
		}
		return a.SourceIndex < b.SourceIndex
	})
}
