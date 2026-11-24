package remux

import (
	"mediapipeline/internal/demux"
	"mediapipeline/internal/track"
)

// stblBox copies the input stsd verbatim and rebuilds the timing/size/offset
// tables around it.  Every sample is its own chunk, so stsc has exactly one
// entry and the chunk offsets map one-to-one onto samples.
func stblBox(tr demux.Track, sizes []uint32, chunks []uint32) []byte {
	var b boxBuilder
	s := b.start("stbl")
	if len(tr.Stsd) > 0 {
		b.raw(tr.Stsd)
	} else {
		b.raw(synthesizedStsd(handlerOf(tr)))
	}

	// stts: consecutive equal deltas are grouped without changing meaning.
	stts := b.start("stts")
	b.full(0, 0)
	type run struct{ count, delta uint32 }
	var runs []run
	for _, sample := range tr.Samples {
		if n := len(runs); n > 0 && runs[n-1].delta == sample.Delta {
			runs[n-1].count++
		} else {
			runs = append(runs, run{count: 1, delta: sample.Delta})
		}
	}
	b.u32(uint32(len(runs)))
	for _, r := range runs {
		b.u32(r.count)
		b.u32(r.delta)
	}
	b.finish(stts)

	hasCTS := false
	for _, sample := range tr.Samples {
		if sample.CTS.Ticks != sample.DTS.Ticks {
			hasCTS = true
			break
		}
	}
	if hasCTS {
		ctts := b.start("ctts")
		b.full(1, 0)
		var entries []struct{ count, off uint32 }
		for _, sample := range tr.Samples {
			off := int32(sample.CTS.Ticks - sample.DTS.Ticks)
			if n := len(entries); n > 0 && int32(entries[n-1].off) == off {
				entries[n-1].count++
			} else {
				entries = append(entries, struct{ count, off uint32 }{count: 1, off: uint32(off)})
			}
		}
		b.u32(uint32(len(entries)))
		for _, e := range entries {
			b.u32(e.count)
			b.u32(e.off)
		}
		b.finish(ctts)
	}

	// stss only when the track is not all-sync.
	syncs := make([]uint32, 0, len(tr.Samples))
	allSync := true
	for i, sample := range tr.Samples {
		if isSyncSample(tr, sample) {
			syncs = append(syncs, uint32(i+1))
		} else {
			allSync = false
		}
	}
	if !allSync && len(tr.Samples) > 0 && tr.Kind == track.KindVideo {
		stss := b.start("stss")
		b.full(0, 0)
		b.u32(uint32(len(syncs)))
		for _, n := range syncs {
			b.u32(n)
		}
		b.finish(stss)
	}

	stsc := b.start("stsc")
	b.full(0, 0)
	if len(chunks) > 0 {
		b.u32(1)
		b.u32(1)
		b.u32(1)
		b.u32(1)
	} else {
		b.u32(0)
	}
	b.finish(stsc)

	stsz := b.start("stsz")
	b.full(0, 0)
	b.u32(0)
	b.u32(uint32(len(sizes)))
	for _, sz := range sizes {
		b.u32(sz)
	}
	b.finish(stsz)

	stco := b.start("stco")
	b.full(0, 0)
	b.u32(uint32(len(chunks)))
	for _, c := range chunks {
		b.u32(c)
	}
	b.finish(stco)
	b.finish(s)
	return b.bytes()
}

func isSyncSample(tr demux.Track, s demux.Sample) bool {
	_ = tr
	return s.Sync
}
