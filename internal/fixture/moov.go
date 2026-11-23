package fixture

import (
	"github.com/broadcast/mediad/internal/model"
)

// writeMoov emits ftyp-less movie metadata. mdatOffset is the absolute file
// offset where payload begins, used to fill stco entries.
func (b *builder) writeMoov(s Spec, layouts []trackLayout, mdatOffset uint32) {
	closeMoov := b.box("moov")
	b.writeMvhd(s, layouts)
	for _, l := range layouts {
		b.writeTrak(s, l, mdatOffset)
	}
	closeMoov()
}

func (b *builder) writeMvhd(s Spec, layouts []trackLayout) {
	closeBox := b.box("mvhd")
	b.fullBox(0, 0)
	b.u32(0) // creation_time
	b.u32(0) // modification_time
	b.u32(1000)
	var maxDur uint32
	for _, l := range layouts {
		scaled := uint64(l.totalUnits) * 1000 / uint64(l.spec.Timescale)
		if uint32(scaled) > maxDur {
			maxDur = uint32(scaled)
		}
	}
	b.u32(maxDur)
	b.u32(0x00010000) // rate 1.0
	b.u16(0x0100)     // volume
	b.zeros(10)       // reserved
	b.identityMatrix()
	b.zeros(24)       // pre_defined
	b.u32(2)          // next_track_ID
	closeBox()
}

func (b *builder) identityMatrix() {
	b.u32(0x00010000)
	b.u32(0)
	b.u32(0)
	b.u32(0)
	b.u32(0x00010000)
	b.u32(0)
	b.u32(0)
	b.u32(0)
	b.u32(0x40000000)
}

func (b *builder) writeTrak(s Spec, l trackLayout, mdatOffset uint32) {
	closeTrak := b.box("trak")
	b.writeTkhd(l)
	if l.spec.StartUnits > 0 {
		b.writeEdts(l)
	}
	b.writeMdia(s, l, mdatOffset)
	closeTrak()
}

// writeEdts emits an empty edit that shifts the first presented sample to
// StartUnits, modeling a non-zero-start timeline.
func (b *builder) writeEdts(l trackLayout) {
	closeEdts := b.box("edts")
	closeElst := b.box("elst")
	b.fullBox(0, 0)
	b.u32(1) // entry_count
	b.u32(l.totalUnits)
	b.i32(int32(l.spec.StartUnits))
	b.u16(0x0001) // media_rate_integer
	b.u16(0)      // media_rate_fraction
	closeElst()
	closeEdts()
}

func (b *builder) writeTkhd(l trackLayout) {
	closeBox := b.box("tkhd")
	b.fullBox(0, 0x000003) // track_enabled | in_movie | in_preview
	b.u32(0)
	b.u32(0)
	b.u32(l.spec.ID)
	b.u32(0) // reserved
	b.u32(l.totalUnits)
	b.zeros(8) // reserved
	b.u16(0)   // layer
	b.u16(0)   // alternate_group
	if l.spec.Kind == model.KindAudio {
		b.u16(0x0100)
	} else {
		b.u16(0)
	}
	b.u16(0) // reserved
	b.identityMatrix()
	if l.spec.Kind == model.KindVideo {
		b.u32(uint32(l.spec.Width) << 16)
		b.u32(uint32(l.spec.Height) << 16)
	} else {
		b.zeros(8)
	}
	closeBox()
}

func (b *builder) writeMdia(s Spec, l trackLayout, mdatOffset uint32) {
	closeMdia := b.box("mdia")
	b.writeMdhd(l)
	b.writeHdlr(l)
	b.writeMinf(s, l, mdatOffset)
	closeMdia()
}

func (b *builder) writeMdhd(l trackLayout) {
	closeBox := b.box("mdhd")
	b.fullBox(0, 0)
	b.u32(0)
	b.u32(0)
	b.u32(l.spec.Timescale)
	b.u32(l.totalUnits)
	b.u16(0x55C4) // undetermined language 'und'
	b.u16(0)      // pre_defined
	closeBox()
}

func (b *builder) writeHdlr(l trackLayout) {
	closeBox := b.box("hdlr")
	b.fullBox(0, 0)
	b.u32(0) // pre_defined
	switch l.spec.Kind {
	case model.KindVideo:
		b.fourcc("vide")
	case model.KindAudio:
		b.fourcc("soun")
	default:
		b.fourcc("meta")
	}
	b.zeros(12) // reserved
	b.raw(append([]byte("mediad fixture"), 0))
	closeBox()
}
