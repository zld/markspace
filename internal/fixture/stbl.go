package fixture

import (
	"github.com/broadcast/mediad/internal/model"
)

func (b *builder) writeMinf(s Spec, l trackLayout, mdatOffset uint32) {
	closeMinf := b.box("minf")
	if l.spec.Kind == model.KindVideo {
		closeVmhd := b.box("vmhd")
		b.fullBox(0, 1)
		b.u16(0)
		b.u16(0)
		b.u16(0)
		b.u16(0)
		closeVmhd()
	} else {
		closeSmhd := b.box("smhd")
		b.fullBox(0, 0)
		b.u16(0)
		b.u16(0)
		closeSmhd()
	}
	b.writeDinf()
	b.writeStbl(s, l, mdatOffset)
	closeMinf()
}

func (b *builder) writeDinf() {
	closeDinf := b.box("dinf")
	closeDref := b.box("dref")
	b.fullBox(0, 0)
	b.u32(1) // entry_count
	closeUrl := b.box("url ")
	b.fullBox(0, 1) // self-contained flag
	closeUrl()
	closeDref()
	closeDinf()
}

func (b *builder) writeStbl(s Spec, l trackLayout, mdatOffset uint32) {
	closeStbl := b.box("stbl")
	b.writeStsd(s, l)
	b.writeStts(l)
	b.writeStsc(l)
	b.writeStsz(l)
	b.writeStco(l, mdatOffset)
	if len(l.keyframes) > 0 {
		b.writeStss(l)
	}
	closeStbl()
}

func (b *builder) writeStsd(s Spec, l trackLayout) {
	closeStsd := b.box("stsd")
	b.fullBox(0, 0)
	b.u32(1) // entry_count
	if l.spec.Kind == model.KindVideo {
		b.writeAvc1(s, l)
	} else {
		b.writeMp4a(l)
	}
	closeStsd()
}

func (b *builder) writeAvc1(s Spec, l trackLayout) {
	closeEntry := b.box("avc1")
	b.zeros(6)          // reserved
	b.u16(1)            // data_reference_index
	b.u16(0)            // pre_defined
	b.u16(0)            // reserved
	b.zeros(12)         // pre_defined[3]
	b.u16(l.spec.Width)
	b.u16(l.spec.Height)
	b.u32(0x00480000)   // horizresolution 72dpi
	b.u32(0x00480000)   // vertresolution
	b.u32(0)            // reserved
	b.u16(1)            // frame_count
	b.zeros(32)         // compressorname
	b.u16(0x0018)       // depth
	b.u16(0xFFFF)       // pre_defined
	if l.spec.ColorPrimaries != 0 {
		b.writeColr(l)
	}
	closeEntry()
}

func (b *builder) writeColr(l trackLayout) {
	closeColr := b.box("colr")
	b.fourcc("nclx")
	b.u16(l.spec.ColorPrimaries)
	b.u16(l.spec.ColorTransfer)
	b.u16(l.spec.ColorMatrix)
	b.u8(0) // full_range_flag=0 + reserved 7 bits
	closeColr()
}

func (b *builder) writeMp4a(l trackLayout) {
	closeEntry := b.box("mp4a")
	b.zeros(6) // reserved
	b.u16(1)   // data_reference_index
	b.zeros(8) // reserved (version+qualifier)
	b.u16(l.spec.ChannelCount)
	b.u16(16) // sample_size
	b.u16(0)  // pre_defined
	b.u16(0)  // reserved
	b.u32(l.spec.SampleRate << 16)
	closeEntry()
}

func (b *builder) writeStts(l trackLayout) {
	closeBox := b.box("stts")
	b.fullBox(0, 0)
	// Run-length encode identical adjacent deltas.
	type run struct{ count, delta uint32 }
	var runs []run
	for _, d := range l.deltas {
		if n := len(runs); n > 0 && runs[n-1].delta == d {
			runs[n-1].count++
		} else {
			runs = append(runs, run{1, d})
		}
	}
	b.u32(uint32(len(runs)))
	for _, r := range runs {
		b.u32(r.count)
		b.u32(r.delta)
	}
	closeBox()
}

func (b *builder) writeStsc(l trackLayout) {
	closeBox := b.box("stsc")
	b.fullBox(0, 0)
	b.u32(1) // one run: every chunk has one sample
	b.u32(1) // first_chunk
	b.u32(1) // samples_per_chunk
	b.u32(1) // sample_description_index
	closeBox()
}

func (b *builder) writeStsz(l trackLayout) {
	closeBox := b.box("stsz")
	b.fullBox(0, 0)
	b.u32(0) // sample_size=0 => per-sample table
	b.u32(uint32(len(l.sizes)))
	for _, sz := range l.sizes {
		b.u32(sz)
	}
	closeBox()
}

func (b *builder) writeStco(l trackLayout, mdatOffset uint32) {
	closeBox := b.box("stco")
	b.fullBox(0, 0)
	b.u32(uint32(len(l.chunks)))
	cursor := uint64(mdatOffset)
	for _, ch := range l.chunks {
		b.u32(uint32(cursor))
		for i := 0; i < ch.count; i++ {
			cursor += uint64(l.sizes[ch.firstSample+i])
		}
	}
	closeBox()
}

func (b *builder) writeStss(l trackLayout) {
	closeBox := b.box("stss")
	b.fullBox(0, 0)
	b.u32(uint32(len(l.keyframes)))
	for _, n := range l.keyframes {
		b.u32(n)
	}
	closeBox()
}

var _ = model.KindVideo
