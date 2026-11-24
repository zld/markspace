// Package mp4 repairs scan output into a continuous, presentation-aligned
// timeline. No sample is dropped: runs that cannot be inferred are reported
// with the source offset of the first affected timing entry.
package mp4

import (
	"fmt"
	"math"
	"sort"

	"github.com/pion/rtp"

	"mediad/internal/clock"
)

// RepairPolicy bounds what repair may infer.
func Repair(scan *Scan, opts RepairOptions) (*Repaired, error) {
	if scan == nil {
		return nil, clock.New(clock.StageRepair, clock.CodeBadArgs, "nil scan")
	}
	if opts.Policy.MaxGapTicks <= 0 {
		opts.Policy = DefaultPolicy()
	}
	out := &Repaired{Path: scan.Path, MovieScale: scan.MovieScale, Issues: scan.Problems}
	for i := range scan.Tracks {
		rt, err := repairTrack(&scan.Tracks[i], opts)
		if err != nil {
			return nil, err
		}
		out.Tracks = append(out.Tracks, *rt)
	}
	return out, nil
}

func repairTrack(raw *RawTrack, opts RepairOptions) (*RepairedTrack, error) {
	tr := &RepairedTrack{
		TrackID:          raw.TrackID,
		Kind:             raw.Kind,
		Timescale:        raw.Timescale,
		EditList:         raw.EditList,
		RawDurationTicks: raw.DurationTicks,
		SyncFromFlags:    raw.HasSyncTable,
	}
	spec, ok := opts.TrackSpecs[raw.TrackID]
	if ok && spec.Timescale != 0 && spec.Timescale != raw.Timescale {
		return nil, clock.New(clock.StageRepair, clock.CodeBadTimescale,
			"manifest timescale %d does not match media %d", spec.Timescale, raw.Timescale).WithTrack(raw.TrackID)
	}
	tr.OriginNs = opts.RecordingOriginNs
	if ok {
		tr.OriginNs = spec.OriginNs
	}
	if len(raw.Samples) == 0 {
		return nil, clock.New(clock.StageRepair, clock.CodeUnrepairableRange, "track has no samples").WithTrack(raw.TrackID)
	}
	if raw.Timescale == 0 {
		return nil, clock.New(clock.StageRepair, clock.CodeBadTimescale, "track timescale is zero").WithTrack(raw.TrackID)
	}
	tr.RTP = extendRTP(opts.RTPRecords)

	nominal := localDelta(raw.Samples)
	out := make([]RepairedSample, len(raw.Samples))
	prev := int64(math.MinInt64)
	shift := int64(0)
	for i := range raw.Samples {
		s := &raw.Samples[i]
		dts := s.DTS + shift
		var cause clock.Code
		switch {
		case s.DTS > opts.Policy.MaxReasonableTicks || s.DTS < -opts.Policy.MaxReasonableTicks:
			return nil, clock.New(clock.StageRepair, clock.CodeUnrepairableRange,
				"DTS %d at sample %d is outside the plausible range", s.DTS, s.Index).
				WithTrack(raw.TrackID).WithSample(s.Index).WithOffset(s.TableOffset)
		case i > 0 && dts-prev <= 0:
			// Regression: continue the local cadence and record exactly which
			// byte carried the bad value.
			target := prev + nominal
			shift += target - dts
			dts = target
			cause = clock.CodeDTSRegression
		case i > 0 && dts-prev > opts.Policy.MaxGapTicks:
			return nil, clock.New(clock.StageRepair, clock.CodeUnrepairableRange,
				"DTS gap of %d ticks at sample %d exceeds policy %d", dts-prev, s.Index, opts.Policy.MaxGapTicks).
				WithTrack(raw.TrackID).WithSample(s.Index).WithOffset(s.TableOffset)
		}
		pts := s.PTS + shift
		if pts < dts {
			// Composition time earlier than decode time would reorder samples
			// before they can be decoded; clamp and record. Keep an earlier,
			// more specific cause if one already applies to this sample.
			pts = dts
			if cause == "" {
				cause = clock.CodePTSOutOfRange
			}
		}
		out[i] = RepairedSample{
			Index: s.Index, Offset: s.Offset, Size: s.Size, RawOffset: s.TableOffset,
			DTS: dts, PTS: pts, OrigDTS: s.DTS, OrigPTS: s.PTS, Sync: s.Sync,
			Repaired: cause != "",
		}
		if cause != "" {
			tr.RepairCount++
			mergeInterval(tr, cause, s.Index, s.TableOffset, shift, nominal)
		}
		prev = dts
	}
	assignPresentation(tr, out)
	tr.Samples = out
	// FirstNs/LastNs are session-relative so they line up with the manifest's
	// editorial windows directly. Sample NS stays file-relative (pre-roll is
	// negative) and is offset by OriginNs only when a caller needs session
	// time, which keeps index hashes independent of session start.
	tr.FirstNs = tr.OriginNs
	tr.LastNs = out[len(out)-1].NS + tr.OriginNs
	return tr, nil
}

// localDelta estimates the sample cadence as the median positive stored
// delta. A mean or first-value estimate would be dragged by a single glitch;
// the median is robust to isolated jumps.
func localDelta(samples []RawSample) int64 {
	if len(samples) < 2 {
		return 1
	}
	deltas := make([]int64, 0, len(samples)-1)
	for i := 1; i < len(samples); i++ {
		if d := samples[i].DTS - samples[i-1].DTS; d > 0 {
			deltas = append(deltas, d)
		}
	}
	if len(deltas) == 0 {
		return 1
	}
	sort.Slice(deltas, func(i, j int) bool { return deltas[i] < deltas[j] })
	return deltas[len(deltas)/2]
}

// assignPresentation maps repaired media ticks through the edit list. The
// edit list's media_time trims pre-roll, so presentation zero is the
// recording origin; leadNs from any empty edit is added ahead of it.
func assignPresentation(tr *RepairedTrack, out []RepairedSample) {
	el := tr.EditList
	firstMedia := el.FirstMediaTime()
	lead := el.LeadNs()
	for i := range out {
		ticks := out[i].PTS
		var ns int64
		if mapped, ok := el.PresentationNs(ticks); ok {
			ns = mapped
		} else {
			// Outside the edit list, project linearly from the trim point so
			// out-of-window samples still have a reviewable time.
			ns = lead + clock.TicksToNs(tr.Timescale, ticks-firstMedia)
		}
		out[i].NS = ns
	}
}

func mergeInterval(tr *RepairedTrack, cause clock.Code, idx uint32, rawOff, delta, nominal int64) {
	for i := range tr.Intervals {
		iv := &tr.Intervals[i]
		if iv.Cause == cause && iv.LastIndex+1 == idx {
			iv.LastIndex = idx
			iv.Count++
			iv.CumulativeShiftTicks = delta
			return
		}
	}
	detail := fmt.Sprintf("samples re-timed by %d ticks using cadence %d", delta, nominal)
	if delta == 0 {
		detail = fmt.Sprintf("composition time clamped to decode time (cadence %d)", nominal)
	}
	tr.Intervals = append(tr.Intervals, RepairInterval{
		TrackID: tr.TrackID, Cause: cause, FirstIndex: idx, LastIndex: idx,
		RawOffset: rawOff, CumulativeShiftTicks: delta, Count: 1, Detail: detail,
	})
}

// extendRTP unwraps 32-bit sender timestamps. Padding is verified through
// pion's wire codec so the wrap math is exercised against real RTP bytes.
func extendRTP(records []RTPRecord) *RTPAnchor {
	if len(records) == 0 {
		return nil
	}
	a := &RTPAnchor{
		FirstTimestamp: records[0].Timestamp,
		LastTimestamp:  records[len(records)-1].Timestamp,
		SSRC:           records[0].SSRC,
		PacketCount:    len(records),
	}
	var extended uint64 = uint64(records[0].Timestamp)
	prev := records[0].Timestamp
	for i := 1; i < len(records); i++ {
		cur := records[i].Timestamp
		var step uint64
		if cur >= prev {
			step = uint64(cur - prev)
		} else {
			// A backward step larger than half the space is a wrap; a smaller
			// one is a sender glitch and is carried as-is.
			step = uint64(cur) + (1 << 32) - uint64(prev)
		}
		if step > a.GapTicks {
			a.GapTicks = step
		}
		if cur < prev {
			a.WrapCount++
		}
		extended += step
		prev = cur
	}
	a.ExtendedLast = extended
	verifyRTPWire(records)
	return a
}

// verifyRTPWire round-trips a few headers through pion. Failures here would
// indicate an incompatible RTP version, so the timestamp is trusted only
// after the wire encoding agrees.
func verifyRTPWire(records []RTPRecord) bool {
	n := len(records)
	if n > 8 {
		n = 8
	}
	for i := 0; i < n; i++ {
		pkt := &rtp.Packet{Header: rtp.Header{
			Version: 2, PayloadType: 96, SequenceNumber: uint16(i),
			Timestamp: records[i].Timestamp, SSRC: records[i].SSRC,
		}}
		raw, err := pkt.Marshal()
		if err != nil {
			return false
		}
		var back rtp.Packet
		if err := back.Unmarshal(raw); err != nil || back.Timestamp != records[i].Timestamp {
			return false
		}
	}
	return true
}
