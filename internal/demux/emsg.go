package demux

import (
	"mediapipeline/internal/clock"
	"mediapipeline/internal/fault"
	"mediapipeline/internal/metadata"
)

// collectEmsgEvents parses every emsg box in the input.  Version 0 events are
// relative to the earliest presentation time of their segment, which is taken
// from the containing (or preceding) moof's tfdt; version 1 events carry an
// absolute presentation time.  Container-level events use track id 0.
func collectEmsgEvents(top []*rawBox) ([]metadata.Event, error) {
	var out []metadata.Event
	var lastBase int64
	var lastTimescale uint32
	for _, b := range top {
		base, timescale := lastBase, lastTimescale
		if b.typ == "moof" {
			if v, ts, ok := fragmentBaseTime(b); ok {
				base, timescale = v, ts
			}
		}
		boxes := []*rawBox{b}
		if b.typ == "moof" {
			boxes = append(boxes, b.findAll("emsg")...)
		}
		for _, emsg := range boxes {
			if emsg.typ != "emsg" {
				continue
			}
			ev, err := parseEmsg(emsg, base, timescale)
			if err != nil {
				return nil, err
			}
			out = append(out, ev)
		}
		if b.typ == "moof" {
			if v, ts, ok := fragmentBaseTime(b); ok {
				lastBase, lastTimescale = v, ts
			}
		}
	}
	// emsg boxes are also valid inside moov (before any media).  Their version
	// 0 presentation time is then relative to zero.
	for _, moov := range findTops(top, "moov") {
		for _, emsg := range moov.findAll("emsg") {
			ev, err := parseEmsg(emsg, 0, 0)
			if err != nil {
				return nil, err
			}
			out = append(out, ev)
		}
	}
	sortEventsByOffset(out)
	return out, nil
}

func sortEventsByOffset(events []metadata.Event) {
	for i := 1; i < len(events); i++ {
		for j := i; j > 0 && events[j].SourceOffset < events[j-1].SourceOffset; j-- {
			events[j], events[j-1] = events[j-1], events[j]
		}
	}
}

// fragmentBaseTime returns the first tfdt base decode time and its timescale.
func fragmentBaseTime(moof *rawBox) (int64, uint32, bool) {
	for _, traf := range moof.findAll("traf") {
		tfdt := traf.find("tfdt")
		if tfdt == nil {
			continue
		}
		r := newBinReader(tfdt, fault.OpBoxHeader)
		version, _, err := r.fullBox()
		if err != nil {
			return 0, 0, false
		}
		if version == 1 {
			v, err := r.u64()
			if err != nil {
				return 0, 0, false
			}
			return int64(v), 0, true
		}
		v, err := r.u32()
		if err != nil {
			return 0, 0, false
		}
		return int64(v), 0, true
	}
	return 0, 0, false
}

func parseEmsg(b *rawBox, base int64, baseTimescale uint32) (metadata.Event, error) {
	r := newBinReader(b, fault.OpBoxHeader)
	version, _, err := r.fullBox()
	if err != nil {
		return metadata.Event{}, err
	}
	ev := metadata.Event{Kind: metadata.KindEmsg, TrackID: 0, SourceOffset: b.off}
	if version == 0 {
		scheme, err := readCString(r)
		if err != nil {
			return ev, err
		}
		value, err := readCString(r)
		if err != nil {
			return ev, err
		}
		ts, err := r.u32()
		if err != nil {
			return ev, err
		}
		delta, err := r.u32()
		if err != nil {
			return ev, err
		}
		if _, err := r.u32(); err != nil { // event duration
			return ev, err
		}
		if _, err := r.u32(); err != nil { // id
			return ev, err
		}
		data, err := r.bytes(len(r.b) - r.pos)
		if err != nil {
			return ev, err
		}
		refTicks := base
		if baseTimescale != 0 && baseTimescale != ts {
			refTicks = clock.New(base, baseTimescale).MustRescale(ts)
		}
		ev.Scheme, ev.Value, ev.Data = scheme, value, data
		ev.Time = clock.New(refTicks+int64(delta), ts)
		return ev, nil
	}
	ts, err := r.u32()
	if err != nil {
		return ev, err
	}
	t, err := r.u64()
	if err != nil {
		return ev, err
	}
	if _, err := r.u32(); err != nil { // event duration
		return ev, err
	}
	if _, err := r.u32(); err != nil { // id
		return ev, err
	}
	scheme, err := readCString(r)
	if err != nil {
		return ev, err
	}
	value, err := readCString(r)
	if err != nil {
		return ev, err
	}
	data, err := r.bytes(len(r.b) - r.pos)
	if err != nil {
		return ev, err
	}
	ev.Scheme, ev.Value, ev.Data = scheme, value, data
	ev.Time = clock.New(int64(t), ts)
	return ev, nil
}

func readCString(r *binReader) (string, error) {
	for i := r.pos; i < len(r.b); i++ {
		if r.b[i] == 0 {
			s := string(r.b[r.pos:i])
			r.pos = i + 1
			return s, nil
		}
	}
	return "", r.fail("unterminated string")
}
