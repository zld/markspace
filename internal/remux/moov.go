package remux

import (
	"mediapipeline/internal/clock"
)

// buildMoov assembles a complete moov from the demuxed tracks, the mapping and
// the relocated chunk offsets.
func buildMoov(plan Plan, md *media, chapterUdta []byte, timescale uint32) []byte {
	duration := movieDuration(plan, timescale)
	var mvhd boxBuilder
	mo := mvhd.start("mvhd")
	mvhd.full(0, 0)
	mvhd.u32(0) // creation
	mvhd.u32(0) // modification
	mvhd.u32(timescale)
	mvhd.u32(uint32(duration))
	mvhd.u32(0x00010000) // rate 1.0
	mvhd.u16(0x0100)     // volume 1.0
	mvhd.u16(0)
	mvhd.zeros(8)
	writeMatrix(&mvhd)
	mvhd.zeros(24)
	nextTrack := uint32(1)
	for _, tr := range plan.Tracks {
		if m, ok := plan.Mapping.Lookup(tr.ID); ok && m.OutputID >= nextTrack {
			nextTrack = m.OutputID + 1
		}
	}
	mvhd.u32(nextTrack)
	mvhd.finish(mo)

	var body boxBuilder
	body.raw(mvhd.bytes())
	for _, tr := range plan.Tracks {
		mapped, _ := plan.Mapping.Lookup(tr.ID)
		outTrack, ok := md.trackByID[mapped.OutputID]
		if !ok {
			outTrack = tr
		}
		body.raw(trakBox(outTrack, mapped, md, timescale))
	}
	if len(chapterUdta) > 0 {
		body.raw(chapterUdta)
	}

	var moov boxBuilder
	off := moov.start("moov")
	moov.raw(body.bytes())
	moov.finish(off)
	return moov.bytes()
}

func writeMatrix(b *boxBuilder) {
	for _, v := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
		b.u32(v)
	}
}

func movieDuration(plan Plan, timescale uint32) uint64 {
	var max uint64
	for _, tr := range plan.Tracks {
		var ticks uint64
		for _, s := range tr.Samples {
			ticks += uint64(s.Delta)
		}
		if tr.Timescale == 0 {
			continue
		}
		start := trackStartTicks(tr)
		var startMovie int64
		if start > 0 {
			startMovie, _, _ = clock.New(start, tr.Timescale).Rescale(timescale)
		}
		v := uint64(startMovie) + ticks*uint64(timescale)/uint64(tr.Timescale)
		if v > max {
			max = v
		}
	}
	if len(plan.Events.Events) > 0 {
		last := plan.Events.Events[len(plan.Events.Events)-1]
		if ticks, err := eventTime(last.Time, timescale); err == nil && uint64(ticks) > max {
			max = uint64(ticks)
		}
	}
	return max
}
