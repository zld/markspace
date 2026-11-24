package index

import (
	"sort"

	"mediasync/internal/clock"
	"mediasync/internal/rtp"
)

// Rebuild constructs random-access records from recovered media intervals.
// One record is emitted per interval start plus one for every concealment
// boundary inside an interval, so the number of entries equals the number of
// valid, time-sorted access points of the repaired timeline. Offsets are
// computed from frame geometry and are guaranteed not to exceed inputLength.
func Rebuild(intervals []rtp.Interval, gaps []rtp.Gap, inputLength int64,
	blockAlign, framesPerPacket int) ([]Record, error) {
	if blockAlign <= 0 || framesPerPacket <= 0 {
		return nil, clock.NewError(clock.ErrInvalidArgument,
			"blockAlign=%d framesPerPacket=%d invalid", blockAlign, framesPerPacket)
	}
	// Quick pre-check that the full stream geometry fits both the input and
	// the 5-million-record limit before any allocation.
	totalFrames := int64(0)
	for _, iv := range intervals {
		totalFrames += (iv.EndSeq - iv.StartSeq + 1) * int64(framesPerPacket)
	}
	if totalFrames*int64(blockAlign) > inputLength {
		return nil, clock.ErrorAt(clock.ErrOverflow, 0,
			"interval geometry needs %d bytes but input has %d",
			totalFrames*int64(blockAlign), inputLength)
	}

	concealed := map[int64]bool{}
	for _, g := range gaps {
		if g.Kind == rtp.GapConcealed {
			for s := g.StartSeq; s <= g.EndSeq; s++ {
				concealed[s] = true
			}
		}
	}

	var recs []Record
	frameCursor := int64(0)
	for _, iv := range intervals {
		ivStartFrame := frameCursor
		packets := iv.EndSeq - iv.StartSeq + 1
		step := clock.Tick(0)
		if packets > 0 {
			step = (iv.EndTS - iv.StartTS) / clock.Tick(packets)
		}
		for s := iv.StartSeq; s <= iv.EndSeq; s++ {
			frame := ivStartFrame + (s-iv.StartSeq)*int64(framesPerPacket)
			ts := iv.StartTS + step*clock.Tick(s-iv.StartSeq)
			recs = append(recs, Record{
				ID:         int64(len(recs)),
				TimeTicks:  ts,
				TimeMs:     ts.Milli(),
				ByteOffset: frame * int64(blockAlign),
				FrameIndex: frame,
				Length:     int64(framesPerPacket) * int64(blockAlign),
				Concealed:  concealed[s],
			})
		}
		frameCursor += packets * int64(framesPerPacket)
		if int64(len(recs)) > Limit {
			return nil, clock.ErrorAt(clock.ErrLimitExceeded, 0,
				"index exceeds %d records", Limit)
		}
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].TimeTicks < recs[j].TimeTicks })
	for i := range recs {
		recs[i].ID = int64(i)
	}
	return recs, nil
}

// Sanitize validates an existing (possibly corrupted) record set and returns
// the salvageable subset in time order together with precise drop counts.
// Invalid, duplicate or out-of-bounds records are dropped; valid offsets are
// guaranteed to stay within inputLength.
func Sanitize(in []Record, inputLength int64, blockAlign int) ([]Record, DropReport) {
	report := DropReport{}
	seenTime := make(map[clock.Tick]bool, len(in))
	seenOffset := make(map[int64]bool, len(in))
	out := make([]Record, 0, len(in))

	for _, rec := range in {
		err := rec.Validate(inputLength, blockAlign)
		if err != nil {
			if me, ok := err.(*clock.MediaError); ok && me.Code == clock.ErrOverflow {
				report.OutOfRange++
			} else {
				report.Corrupt++
			}
			continue
		}
		if seenTime[rec.TimeTicks] || seenOffset[rec.ByteOffset] {
			report.Duplicate++
			continue
		}
		seenTime[rec.TimeTicks] = true
		seenOffset[rec.ByteOffset] = true
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TimeTicks < out[j].TimeTicks })
	for i := range out {
		out[i].ID = int64(i)
	}
	report.TotalDropped = report.OutOfRange + report.Duplicate + report.Corrupt
	return out, report
}
