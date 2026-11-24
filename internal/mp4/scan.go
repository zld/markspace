package mp4

import (
	"bytes"
	"encoding/binary"

	gomp4 "github.com/abema/go-mp4"

	"github.com/zld/avpipe/internal/fault"
)

// Scan parses generated MP4 boxes without panicking on truncation. A report is
// always returned; corruption is represented in the report and its faults.
func Scan(data []byte, representation string) *Report {
	r := &Report{Representation: representation, DataSize: len(data)}
	off := 0
	current := -1
	nextIndexed := false
	var pendingEMSG []EMSGEvent

	for off < len(data) {
		v, err := readBoxAt(data, off, "mp4/scan")
		if err != nil {
			r.addFault(err)
			break
		}
		r.Boxes = append(r.Boxes, BoxInfo{
			Type: v.Type, Offset: int64(v.Offset), Size: int64(v.DeclaredSize), Complete: v.Complete,
		})

		switch v.Type {
		case "moov":
			r.Timescale = parseMoovTimescale(v.Payload)
		case "sidx":
			r.IndexEntries++
			nextIndexed = true
		case "emsg":
			if ev, ok := parseEMSG(v, r); ok {
				pendingEMSG = append(pendingEMSG, ev)
			}
		case "moof":
			seg := parseMoof(v, r)
			seg.Representation = representation
			seg.Timescale = r.Timescale
			seg.Indexed = nextIndexed
			seg.EMSG = append(seg.EMSG, pendingEMSG...)
			pendingEMSG = nil
			r.Segments = append(r.Segments, seg)
			current = len(r.Segments) - 1
			nextIndexed = false
		case "mdat":
			if current >= 0 {
				associateMDAT(v, &r.Segments[current])
			}
			current = -1
		}

		if !v.Complete {
			fe := fault.Truncated("mp4/scan", int64(v.Offset), int64(len(data)), v.Type+" box payload")
			r.addFault(fe)
			if current >= 0 {
				r.Segments[current].Corrupt = true
				if r.Segments[current].FirstUnreadableOffset == 0 {
					r.Segments[current].FirstUnreadableOffset = int64(len(data))
				}
			}
			break
		}
		off = v.PayloadStart + len(v.Payload)
	}

	finalizeReport(r)
	return r
}

func finalizeReport(r *Report) {
	r.MissingIndex = r.IndexEntries == 0
	for i := range r.Segments {
		s := &r.Segments[i]
		if s.Timescale == 0 {
			s.Timescale = r.Timescale
		}
		s.SequenceKnown = s.SequenceKnown || s.Sequence != 0
		s.Readable = s.TimingKnown && s.SequenceKnown && !s.Corrupt && samplesReadable(s.Samples)
		if s.Indexed {
			r.IndexedSegments++
		}
		if r.MissingIndex || !s.Indexed {
			if s.TimingKnown {
				s.RebuiltIndex = true
				r.RebuiltIndex++
			}
		}
		if s.Corrupt && s.TimingKnown {
			r.UnconfirmedRanges = append(r.UnconfirmedRanges, TimeRange{
				StartTicks: s.StartTicks,
				EndTicks:   s.StartTicks + int64(s.DurationTicks),
				Timescale:  s.Timescale,
				Reason:     "corrupt sample data",
			})
		}
	}

	// Find timeline gaps between consecutive readable fragment starts.
	for i := 1; i < len(r.Segments); i++ {
		prev := r.Segments[i-1]
		cur := r.Segments[i]
		if !prev.TimingKnown || !cur.TimingKnown || prev.Timescale != cur.Timescale {
			continue
		}
		expected := prev.StartTicks + int64(prev.DurationTicks)
		if cur.StartTicks > expected {
			r.UnconfirmedRanges = append(r.UnconfirmedRanges, TimeRange{
				StartTicks: expected,
				EndTicks:   cur.StartTicks,
				Timescale:  cur.Timescale,
				Reason:     "missing fragment interval",
			})
		}
	}
}

func samplesReadable(samples []SampleInfo) bool {
	for _, s := range samples {
		if !s.Readable {
			return false
		}
	}
	return true
}

func parseMoovTimescale(payload []byte) uint32 {
	mdhd, ok := findDeep(payload, "mdhd")
	if !ok || len(mdhd.Payload) < 16 {
		return 0
	}
	p := mdhd.Payload
	version := p[0]
	if version == 1 {
		if len(p) < 24 {
			return 0
		}
		return binary.BigEndian.Uint32(p[20:24])
	}
	return binary.BigEndian.Uint32(p[12:16])
}

func parseMoof(v BoxView, r *Report) Segment {
	seg := Segment{BoxOffset: int64(v.Offset)}
	if err := iterBoxes(v.Payload, v.PayloadStart, func(child BoxView) error {
		switch child.Type {
		case "mfhd":
			var mfhd gomp4.Mfhd
			if _, err := gomp4.Unmarshal(bytes.NewReader(child.Payload), uint64(len(child.Payload)), &mfhd, gomp4.Context{}); err != nil {
				r.addFault(fault.Wrap(fault.KindInvalid, err, "parse mfhd"))
			} else {
				seg.Sequence = mfhd.SequenceNumber
				seg.SequenceKnown = true
			}
		case "traf":
			parseTraf(child, &seg, r)
		}
		return nil
	}); err != nil {
		r.addFault(fault.Wrap(fault.KindTruncated, err, "scan moof children"))
		seg.Corrupt = true
	}
	return seg
}

// sampleDuration is scanner-only metadata retained before SampleInfo emission.
type scannedSample struct {
	SampleInfo
	duration uint32
}

func parseTraf(traf BoxView, seg *Segment, r *Report) {
	var scanned []scannedSample
	if err := iterBoxes(traf.Payload, traf.PayloadStart, func(child BoxView) error {
		switch child.Type {
		case "tfdt":
			var tfdt gomp4.Tfdt
			if _, err := gomp4.Unmarshal(bytes.NewReader(child.Payload), uint64(len(child.Payload)), &tfdt, gomp4.Context{}); err != nil {
				r.addFault(fault.Wrap(fault.KindInvalid, err, "parse tfdt"))
				seg.Corrupt = true
			} else {
				seg.StartTicks = int64(tfdt.GetBaseMediaDecodeTime())
				seg.TimingKnown = true
			}
		case "trun":
			var trun gomp4.Trun
			if _, err := gomp4.Unmarshal(bytes.NewReader(child.Payload), uint64(len(child.Payload)), &trun, gomp4.Context{}); err != nil {
				r.addFault(fault.Wrap(fault.KindInvalid, err, "parse trun"))
				seg.Corrupt = true
				return nil
			}
			scanned = make([]scannedSample, 0, len(trun.Entries))
			for i, e := range trun.Entries {
				scanned = append(scanned, scannedSample{
					SampleInfo: SampleInfo{Index: i, Size: e.SampleSize},
					duration:   e.SampleDuration,
				})
			}
		}
		return nil
	}); err != nil {
		r.addFault(fault.Wrap(fault.KindTruncated, err, "scan traf children"))
		seg.Corrupt = true
	}
	seg.Samples = make([]SampleInfo, len(scanned))
	var duration uint32
	for i := range scanned {
		seg.Samples[i] = scanned[i].SampleInfo
		duration += scanned[i].duration
	}
	if seg.DurationTicks == 0 {
		seg.DurationTicks = duration
	}
}

func parseEMSG(v BoxView, r *Report) (EMSGEvent, bool) {
	var emsg gomp4.Emsg
	if _, err := gomp4.Unmarshal(bytes.NewReader(v.Payload), uint64(len(v.Payload)), &emsg, gomp4.Context{}); err != nil {
		r.addFault(fault.Wrap(fault.KindInvalid, err, "parse emsg at offset %d", v.Offset))
		return EMSGEvent{}, false
	}
	return EMSGEvent{
		SchemeIDURI: emsg.SchemeIdUri,
		Value:       emsg.Value,
		Timescale:   emsg.Timescale,
		PTS:         emsg.PresentationTime,
		Duration:    emsg.EventDuration,
		ID:          emsg.Id,
		Message:     emsg.MessageData,
	}, true
}

func associateMDAT(v BoxView, seg *Segment) {
	seg.MdatPayloadOffset = int64(v.PayloadStart)
	seg.MdatPayloadAvailable = len(v.Payload)
	cursor := v.PayloadStart
	availableEnd := v.PayloadStart + len(v.Payload)
	allReadable := len(seg.Samples) > 0
	for i := range seg.Samples {
		size := int(seg.Samples[i].Size)
		seg.Samples[i].Index = i
		seg.Samples[i].Offset = int64(cursor)
		end := cursor + size
		if end > availableEnd {
			seg.Samples[i].Readable = false
			seg.Corrupt = true
			if seg.FirstUnreadableOffset == 0 {
				seg.FirstUnreadableOffset = int64(availableEnd)
			}
			allReadable = false
		} else {
			seg.Samples[i].Readable = true
		}
		cursor = end
	}
	if len(seg.Samples) == 0 {
		allReadable = true
	}
	seg.Readable = seg.TimingKnown && seg.SequenceKnown && !seg.Corrupt && allReadable
}
