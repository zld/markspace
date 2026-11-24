package remux

import (
	"mediapipeline/internal/clock"
	"mediapipeline/internal/demux"
	"mediapipeline/internal/track"
)

// trackStartTicks returns the track's first decode time.  A non-zero start is
// preserved with a leading empty edit, so remuxing never silently moves the
// media timeline to zero.
func trackStartTicks(tr demux.Track) int64 {
	if len(tr.Samples) == 0 {
		return 0
	}
	first := tr.Samples[0].DTS.Ticks
	for _, s := range tr.Samples[1:] {
		if s.DTS.Ticks < first {
			first = s.DTS.Ticks
		}
	}
	return first
}

func trakBox(tr demux.Track, mapped track.Entry, md *media, movieTS uint32) []byte {
	sizes := md.sizes[mapped.OutputID]
	chunks := md.chunks[mapped.OutputID]
	var ticks uint64
	for _, s := range tr.Samples {
		ticks += uint64(s.Delta)
	}
	// tkhd duration covers the whole presentation: any leading empty edit plus
	// the media itself.
	start := trackStartTicks(tr)
	dur1000 := uint64(0)
	if tr.Timescale != 0 {
		startMovie, _, err := clock.New(start, tr.Timescale).Rescale(movieTS)
		if err == nil && startMovie > 0 {
			dur1000 = uint64(startMovie)
		}
		dur1000 += ticks * uint64(movieTS) / uint64(tr.Timescale)
	}

	var tkhd boxBuilder
	t := tkhd.start("tkhd")
	tkhd.full(0, 0x000003) // enabled, in movie
	tkhd.u32(0)
	tkhd.u32(0)
	tkhd.u32(mapped.OutputID)
	tkhd.u32(0)
	tkhd.u32(uint32(dur1000))
	tkhd.zeros(8)
	tkhd.u16(0)
	tkhd.u16(0)
	volume := uint16(0)
	if tr.Kind == track.KindAudio {
		volume = 0x0100
	}
	tkhd.u16(volume)
	tkhd.u16(0)
	writeMatrix(&tkhd)
	tkhd.u32(0)
	tkhd.u32(0)
	tkhd.finish(t)

	var mdhd boxBuilder
	m := mdhd.start("mdhd")
	mdhd.full(0, 0)
	mdhd.u32(0)
	mdhd.u32(0)
	mdhd.u32(mapped.Timescale)
	mdhd.u32(uint32(ticks))
	mdhd.u16(0x55c4) // "und"
	mdhd.u16(0)
	mdhd.finish(m)

	var hdlr boxBuilder
	h := hdlr.start("hdlr")
	hdlr.full(0, 0)
	hdlr.u32(0)
	hdlr.str(handlerOf(tr))
	hdlr.zeros(12)
	hdlr.str("MediaHandler")
	hdlr.u8(0)
	hdlr.finish(h)

	var minfHeader boxBuilder
	var mediaHeader []byte
	switch tr.Kind {
	case track.KindAudio:
		mh := minfHeader.start("smhd")
		minfHeader.full(0, 0)
		minfHeader.u16(0)
		minfHeader.u16(0)
		minfHeader.finish(mh)
		mediaHeader = minfHeader.bytes()
	case track.KindVideo:
		mh := minfHeader.start("vmhd")
		minfHeader.full(0, 1)
		minfHeader.u16(0)
		minfHeader.zeros(6)
		minfHeader.finish(mh)
		mediaHeader = minfHeader.bytes()
	default:
		mh := minfHeader.start("nmhd")
		minfHeader.full(0, 0)
		minfHeader.finish(mh)
		mediaHeader = minfHeader.bytes()
	}

	var dinf boxBuilder
	d := dinf.start("dinf")
	dr := dinf.start("dref")
	dinf.full(0, 0)
	dinf.u32(1)
	url := dinf.start("url ")
	dinf.full(0, 1)
	dinf.finish(url)
	dinf.finish(dr)
	dinf.finish(d)

	stbl := stblBox(tr, sizes, chunks)
	{
		var minf boxBuilder
		mi := minf.start("minf")
		minf.raw(mediaHeader)
		minf.raw(dinf.bytes())
		minf.raw(stbl)
		minf.finish(mi)

		var mdia boxBuilder
		ma := mdia.start("mdia")
		mdia.raw(mdhd.bytes())
		mdia.raw(hdlr.bytes())
		mdia.raw(minf.bytes())
		mdia.finish(ma)

		var trak boxBuilder
		tk := trak.start("trak")
		trak.raw(tkhd.bytes())
		if edts := edtsBox(tr, movieTS); len(edts) > 0 {
			trak.raw(edts)
		}
		trak.raw(mdia.bytes())
		trak.finish(tk)
		return trak.bytes()
	}
}
