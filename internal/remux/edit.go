package remux

import (
	"mediapipeline/internal/clock"
	"mediapipeline/internal/demux"
)

// edtsBox writes the leading empty edit that preserves a non-zero track start.
// The duration is converted to movie ticks exactly; an inexact conversion is
// reported by the caller through validation instead of being rounded.
func edtsBox(tr demux.Track, movieTS uint32) []byte {
	start := trackStartTicks(tr)
	if start == 0 || tr.Timescale == 0 {
		return nil
	}
	movieTicks, exact, err := clock.New(start, tr.Timescale).Rescale(movieTS)
	if err != nil || !exact {
		return nil
	}
	var elst boxBuilder
	off := elst.start("elst")
	elst.full(0, 0)
	elst.u32(2)
	elst.u32(uint32(movieTicks))
	elst.u32(0xffffffff) // media_time = -1: empty edit
	elst.u32(0x00010000)
	// Media edit covering the rest; duration is tkhd's rounded duration, which
	// the demuxer ignores because media_time is zero.
	var mediaTicks uint64
	for _, s := range tr.Samples {
		mediaTicks += uint64(s.Delta)
	}
	mediaMovie := uint64(0)
	if tr.Timescale != 0 {
		mediaMovie = mediaTicks * uint64(movieTS) / uint64(tr.Timescale)
	}
	elst.u32(uint32(mediaMovie))
	elst.u32(0)
	elst.u32(0x00010000)
	elst.finish(off)

	var edts boxBuilder
	eo := edts.start("edts")
	edts.raw(elst.bytes())
	edts.finish(eo)
	return edts.bytes()
}
