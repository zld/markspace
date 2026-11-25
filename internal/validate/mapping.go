package validate

import (
	"fmt"

	"example.com/assetcheck/internal/clock"
	"example.com/assetcheck/internal/fault"
	"example.com/assetcheck/internal/index"
)

// mapPackets aligns the audio track's samples with deliverable packets by
// media time, then records per-sample drift.
//
// Alignment by time (not by list index) is what makes the mapping stable in
// the presence of delivery faults: a missing packet simply leaves one sample
// unmatched, and a duplicate shares its time with the packet it repeats, so
// it is counted once and adds no media time.
//
// Deviation is cumulative drift between the container's declared timeline
// (sample sizes and durations) and the delivered timeline (payload bytes and
// packet durations). A clean asset has zero drift on every sample.
func (r *Result) mapPackets(idx *index.Index, audio *index.Track) {
	pkts := idx.Packets
	pi := 0
	var wantBytes, gotBytes uint64
	var wantDur, gotDur clock.Nanos
	prevByte := int64(0)
	prevTime := clock.Nanos(0)
	for si := range audio.Samples {
		s := &audio.Samples[si]
		for pi < len(pkts) && pkts[pi].Kind == index.PacketDuplicate {
			r.Duplicate++
			pi++
		}
		for pi < len(pkts) && pkts[pi].Kind == index.PacketGap {
			p := &pkts[pi]
			r.Missing += p.GapCount
			r.Faults = append(r.Faults, fault.New(fault.StageValidate, "validate.packet.gap", p.Offset, p.Time,
				fmt.Sprintf("%d packet(s) missing before extended sequence %d", p.GapCount, p.SeqExt)))
			pi++
		}
		wantBytes += uint64(s.Size)
		wantDur = clock.Add(wantDur, s.Duration)
		m := Mapping{
			TrackID: audio.ID,
			Kind:    "audio",
			Sample:  si,
			Offset:  s.Offset,
			DTS:     s.DTS,
			Time:    clock.Invalid,
		}
		if pi < len(pkts) && pkts[pi].Kind == index.PacketOK && pkts[pi].Time == s.DTS {
			p := &pkts[pi]
			m.Packet = p.SeqExt
			m.HasPkt = true
			m.Time = p.Time
			gotBytes += uint64(p.PayloadSize)
			gotDur = clock.Add(gotDur, p.Duration)
			pi++
		} else {
			// No packet carries this sample's media time. It is a hole, not
			// an audio sample: nothing is synthesised for it.
			if pi >= len(pkts) {
				r.Missing++
				m.Note = "no packet remains for this sample"
			} else {
				m.Note = "no packet carries this media time"
			}
			r.Faults = append(r.Faults, fault.New(fault.StageValidate, "validate.packet.missing", s.Offset, s.DTS,
				fmt.Sprintf("audio sample %d at %v has no delivered packet", si, s.DTS)).WithSample(si))
		}
		dev := Deviation{
			TrackID:   audio.ID,
			Kind:      "audio",
			Sample:    si,
			ByteDelta: int64(gotBytes) - int64(wantBytes),
			TimeDelta: clock.Sub(gotDur, wantDur),
		}
		dev.OK = dev.ByteDelta == 0 && dev.TimeDelta == 0
		m.Dev = dev
		// Report only where the drift changes: one located fault at the
		// divergence instead of one per downstream sample.
		if dev.ByteDelta != prevByte || dev.TimeDelta != prevTime {
			r.Faults = append(r.Faults, fault.New(fault.StageValidate, "validate.mapping.deviation", s.Offset, s.DTS,
				fmt.Sprintf("delivery drifts to %d byte(s) and %v at sample %d", dev.ByteDelta, dev.TimeDelta, si)).
				WithSample(si))
			prevByte, prevTime = dev.ByteDelta, dev.TimeDelta
		}
		r.Mappings = append(r.Mappings, m)
	}
	// Any packet the container never accounted for is a delivery surplus.
	for ; pi < len(pkts); pi++ {
		if pkts[pi].Kind == index.PacketDuplicate {
			r.Duplicate++
		}
	}
}

// checkSubtitle confirms subtitle cues cover the dialogue span and that cues
// do not overlap in a way that would make two lines claim one instant.
func (r *Result) checkSubtitle(idx *index.Index, audio *index.Track) {
	sub := idx.SubtitleTrack()
	if sub == nil {
		r.Faults = append(r.Faults, fault.New(fault.StageValidate, "validate.subtitle.missing", fault.NoOffset, clock.Invalid,
			"asset has no subtitle track to cover the dialogue"))
		return
	}
	if len(sub.Samples) == 0 {
		r.Faults = append(r.Faults, fault.New(fault.StageValidate, "validate.subtitle.empty", fault.NoOffset, clock.Invalid,
			"subtitle track has no cues"))
		return
	}
	required := clock.Sub(audio.End, audio.Start)
	cov := Coverage{Required: required}
	var prevEnd clock.Nanos = clock.Invalid
	for si := range sub.Samples {
		s := &sub.Samples[si]
		end := clock.Add(s.DTS, s.Duration)
		if end > audio.End || s.DTS < audio.Start {
			r.Faults = append(r.Faults, fault.New(fault.StageValidate, "validate.subtitle.outside_dialogue", s.Offset, s.DTS,
				fmt.Sprintf("subtitle sample %d (%v..%v) leaves the dialogue range %v..%v", si, s.DTS, end, audio.Start, audio.End)).
				WithSample(si))
		}
		if prevEnd != clock.Invalid && s.DTS < prevEnd {
			cov.Overlaps++
			r.Faults = append(r.Faults, fault.New(fault.StageValidate, "validate.subtitle.overlap", s.Offset, s.DTS,
				fmt.Sprintf("subtitle sample %d starts before the previous cue ends", si)).WithSample(si))
		}
		if prevEnd != clock.Invalid && s.DTS > prevEnd {
			cov.Uncovered += s.DTS - prevEnd
		} else if prevEnd == clock.Invalid && s.DTS > audio.Start {
			cov.Uncovered += s.DTS - audio.Start
		}
		if prevEnd == clock.Invalid || end > prevEnd {
			prevEnd = end
		}
	}
	if prevEnd != clock.Invalid && prevEnd < audio.End {
		cov.Uncovered += audio.End - prevEnd
	}
	if cov.Required > 0 {
		cov.Covered = cov.Required - cov.Uncovered
		if cov.Covered < 0 {
			cov.Covered = 0
		}
	}
	r.Subtitle = cov
	if cov.Uncovered > 0 {
		r.Faults = append(r.Faults, fault.New(fault.StageValidate, "validate.subtitle.coverage", sub.Samples[0].Offset, sub.Start,
			fmt.Sprintf("subtitle cues leave %v of the %v dialogue span uncovered", cov.Uncovered, cov.Required)))
	}
}

// checkSkew compares the packet stream's total media advance with the audio
// track's declared duration. A non-zero skew means dialogue and effects
// would drift apart.
func (r *Result) checkSkew(idx *index.Index) {
	audio := idx.AudioTrack()
	if audio == nil {
		return
	}
	var advance clock.Nanos
	var off int64 = fault.NoOffset
	for i := range idx.Packets {
		p := &idx.Packets[i]
		if p.Kind == index.PacketOK {
			advance = clock.Add(advance, p.Duration)
			if off == fault.NoOffset {
				off = p.Offset
			}
		}
	}
	span := clock.Sub(audio.End, audio.Start)
	skew := clock.Sub(advance, span)
	r.Skew = skew
	if skew != 0 {
		r.Faults = append(r.Faults, fault.New(fault.StageValidate, "validate.skew", off, audio.End,
			fmt.Sprintf("packet stream advances %v while the audio track spans %v (skew %v)", advance, span, skew)))
	}
}
