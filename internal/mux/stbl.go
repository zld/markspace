package mux

import (
	"bytes"
)

func (f *File) buildHdlr(track int) []byte {
	video := f.tracks[track].Video
	return childBox("hdlr", func(b *boxWriter) {
		b.fullBox(0)
		b.u32(0) // pre-defined
		if video {
			b.bytes([]byte("vide"))
		} else {
			b.bytes([]byte("soun"))
		}
		b.zero(12) // reserved
		b.u8(0)    // name (empty c-string)
	})
}

func (f *File) buildMinf(track int, fault Faults) []byte {
	vmhd := childBox("vmhd", func(b *boxWriter) {
		b.fullBox(1)
		b.u16(0) // graphics mode
		b.u16(0)
		b.u16(0)
		b.u16(0) // opcolor
	})
	smhd := childBox("smhd", func(b *boxWriter) {
		b.fullBox(0)
		b.u16(0)
		b.u16(0)
	})
	dinf := childBox("dinf", func(b *boxWriter) {
		b.bytes(childBox("dref", func(c *boxWriter) {
			c.fullBox(0)
			c.u32(1) // entry count
			c.bytes(childBox("url ", func(d *boxWriter) {
				d.fullBox(1) // self-contained flag
			}))
		}))
	})
	stbl := f.buildStbl(track, fault)
	return childBox("minf", func(b *boxWriter) {
		if f.tracks[track].Video {
			b.bytes(vmhd)
		} else {
			b.bytes(smhd)
		}
		b.bytes(dinf)
		b.bytes(stbl)
	})
}

// sampleEntry is a minimal visual/audio entry. Real decoders cannot consume it;
// the demuxer only needs a parseable stsd with a non-zero entry count.
func sampleEntry(video bool) []byte {
	return childBox("mp4v", func(b *boxWriter) {
		b.zero(6) // reserved
		b.u16(1)  // data reference index
		b.zero(16)
		b.u16(320)
		b.u16(240)
		b.u32(0x00480000) // h-res 72 dpi
		b.u32(0x00480000) // v-res
		b.u32(0)
		b.u16(1) // frame count
		b.zero(32)
		b.u16(0x0018) // bit depth
		b.u16(0xFFFF)
	})
}

func (f *File) buildStbl(track int, fault Faults) []byte {
	tr := &f.tracks[track]
	n := uint32(len(tr.Samples))

	stsd := childBox("stsd", func(b *boxWriter) {
		b.fullBox(0)
		b.u32(1)
		b.bytes(sampleEntry(tr.Video))
	})

	stts := childBox("stts", func(b *boxWriter) {
		b.fullBox(0)
		declared := n
		if fault == FaultBadSttsCount {
			declared = n + 3
		}
		b.u32(declared)
		for _, s := range tr.Samples {
			b.u32(1) // one sample per run
			b.u32(s.Delta)
		}
	})

	stsc := childBox("stsc", func(b *boxWriter) {
		b.fullBox(0)
		b.u32(1)
		b.u32(1) // first chunk
		b.u32(1) // one sample per chunk
		b.u32(1) // sample description index
	})

	// FaultTruncateStsz writes a fully parseable stsz box whose entry array
	// covers fewer rows than the stts table declares. go-mp4 unmarshals the box
	// successfully, then the demuxer's explicit count check names the first
	// sample that has no size row, giving a precise track/sample location.
	stszRowCount := n
	if fault == FaultTruncateStsz && n > 4 {
		stszRowCount = n - 3
	}
	stsz := childBox("stsz", func(b *boxWriter) {
		b.fullBox(0)
		b.u32(0)                       // variable sizes
		b.u32(stszRowCount)            // honest, but shorter than stts count
		for i := uint32(0); i < stszRowCount; i++ {
			size := len(tr.Samples[i].Payload)
			if size == 0 {
				size = 64
			}
			b.u32(uint32(size))
		}
	})

	// stco payload is a placeholder; offsets are absolute and patched once the
	// full moov size is known in WriteTo.
	stco := childBox("stco", func(b *boxWriter) {
		b.fullBox(0)
		b.u32(n)
		b.zero(4 * int(n))
	})

	boxes := [][]byte{stsd, stts, stsc, stsz, stco}
	var syncs []uint32
	for i, s := range tr.Samples {
		if s.Sync {
			syncs = append(syncs, uint32(i+1)) // stss numbers samples from 1
		}
	}
	if len(syncs) > 0 {
		stss := childBox("stss", func(b *boxWriter) {
			b.fullBox(0)
			b.u32(uint32(len(syncs)))
			for _, v := range syncs {
				b.u32(v)
			}
		})
		boxes = append(boxes, stss)
	}

	return childBox("stbl", func(b *boxWriter) {
		for _, box := range boxes {
			b.bytes(box)
		}
	})
}

// patchOffsets rewrites every stco entry after moov placement is known. stco
// boxes appear once per trak in track order, so successive matches map to
// successive tracks. base is the absolute file offset of mdat payload.
func (f *File) patchOffsets(moov []byte, base uint64) {
	track := 0
	off := 0
	for {
		idx := indexOf(moov[off:], []byte("stco"))
		if idx < 0 || track >= len(f.chunkOff) {
			return
		}
		pos := off + idx + 4 + 4 + 4 // after fourCC, full box, entry count
		for i, rel := range f.chunkOff[track] {
			abs := base + rel
			for k := 0; k < 4; k++ {
				moov[pos+4*i+(3-k)] = byte(abs >> (8 * k))
			}
		}
		track++
		off += idx + 4
	}
}

func indexOf(haystack, needle []byte) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if bytes.Equal(haystack[i:i+len(needle)], needle) {
			return i
		}
	}
	return -1
}
