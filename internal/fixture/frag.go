package fixture

// fragmented-wrap profile: fMP4 with three moof/mdat pairs. tfdt is
// version 0 (32-bit); the second and third fragments unwrap onto a
// 64-bit timeline, producing exactly one wrap event.
//
// Fragment 1: tfdt 0xFFFFF060, 4 samples x 1000 ticks -> ends at 2^32.
// Fragment 2: tfdt 0          -> unwraps to 2^32 (wrap event).
// Fragment 3: tfdt 4000       -> continues at 2^32 + 4000.

const (
	fragTimescale = 1000
	fragDuration  = 1000
	fragPerFrag   = 4
	fragCount     = 3
)

var fragTfdt = []uint32{0xFFFFF060, 0, 4000}

func fragSample(idx int, key bool) []byte {
	kind := byte('P')
	if key {
		kind = 'I'
	}
	nal := videoSample(idx, kind)
	return nal
}

func trexBox() []byte {
	var p bw
	p.u32(1) // track_ID
	p.u32(1) // default sample description index
	p.u32(fragDuration)
	p.u32(0) // default sample size
	p.u32(0) // default sample flags
	return fullBoxOf("trex", 0, 0, p.buf)
}

func fragMoov() []byte {
	var stbl bw
	stbl.bytes(stsdAvc1(1280, 720))
	stbl.bytes(sttsFrom(nil))
	stbl.bytes(fullBoxOf("stsc", 0, 0, emptyStblEntry()))
	stbl.bytes(stszFrom(nil))
	stbl.bytes(stcoFrom(nil))
	var minf bw
	minf.bytes(vmhd())
	minf.bytes(dinf())
	minf.bytes(boxOf("stbl", stbl.buf))
	var mdia bw
	mdia.bytes(mdhd(fragTimescale, 0))
	mdia.bytes(hdlr("vide"))
	mdia.bytes(boxOf("minf", minf.buf))
	var trak bw
	trak.bytes(tkhd(1, 0, 1280, 720, false))
	trak.bytes(boxOf("mdia", mdia.buf))
	var moov bw
	moov.bytes(mvhd(fragTimescale, 0, 2))
	moov.bytes(boxOf("trak", trak.buf))
	moov.bytes(boxOf("mvex", trexBox()))
	return boxOf("moov", moov.buf)
}

// fragMoof builds one moof. samples are the media payloads; their sizes
// go into the trun. Returns moof bytes and the mdat payload.
func fragMoof(seq uint32, tfdt uint32, sizes []uint32, keys []bool) []byte {
	var mfhd bw
	mfhd.u32(seq)

	var tfhd bw
	tfhd.u32(1) // track_ID
	tfhd.u32(fragDuration)

	var tfdtB bw
	tfdtB.u32(tfdt)

	// trun: data-offset + sample-size + sample-flags.
	var trun bw
	trun.u32(uint32(len(sizes)))
	dataOffsetPos := len(trun.buf)
	trun.u32(0) // patched below
	for i, sz := range sizes {
		trun.u32(sz)
		if keys[i] {
			trun.u32(0x02000000) // depends_on=2: sync
		} else {
			trun.u32(0x01010000) // depends_on=1 + non-sync
		}
	}

	trafBody := func(dataOffset uint32) []byte {
		tb := append([]byte(nil), trun.buf...)
		tb[dataOffsetPos] = byte(dataOffset >> 24)
		tb[dataOffsetPos+1] = byte(dataOffset >> 16)
		tb[dataOffsetPos+2] = byte(dataOffset >> 8)
		tb[dataOffsetPos+3] = byte(dataOffset)
		var traf bw
		traf.bytes(fullBoxOf("tfhd", 0, 0x020008, tfhd.buf)) // default-base-is-moof | default duration
		traf.bytes(fullBoxOf("tfdt", 0, 0, tfdtB.buf))
		traf.bytes(fullBoxOf("trun", 0, 0x000601, tb))
		return boxOf("traf", traf.buf)
	}

	// moof size is fixed once the traf layout is known; compute with a
	// zero offset first, then rebuild with the real data offset.
	probe := trafBody(0)
	moofSize := 8 + len(fullBoxOf("mfhd", 0, 0, mfhd.buf)) + len(probe)
	dataOffset := uint32(moofSize + 8) // moof + mdat header

	var moof bw
	moof.bytes(fullBoxOf("mfhd", 0, 0, mfhd.buf))
	moof.bytes(trafBody(dataOffset))
	return boxOf("moof", moof.buf)
}

func buildFragmentedWrap() []byte {
	var out bw
	out.bytes(ftyp())
	out.bytes(fragMoov())
	for f := 0; f < fragCount; f++ {
		var sizes []uint32
		var keys []bool
		var media bw
		for i := 0; i < fragPerFrag; i++ {
			key := i == 0
			s := fragSample(f*fragPerFrag+i, key)
			sizes = append(sizes, uint32(len(s)))
			keys = append(keys, key)
			media.bytes(s)
		}
		out.bytes(fragMoof(uint32(f+1), fragTfdt[f], sizes, keys))
		out.bytes(boxOf("mdat", media.buf))
	}
	return out.buf
}
