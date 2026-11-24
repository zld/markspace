package remux

import (
	"github.com/abema/go-mp4"
)

// synthesizedStsd builds a minimal stsd for inputs whose demuxer did not carry
// one (MPEG-TS).  The sample entry names the codec so the output re-demuxes to
// the same track kind; no codec private data is invented.  In particular no
// avcC is written, which keeps the payload interpreted as Annex B and
// therefore byte-identical to the input.
func synthesizedStsd(handler string) []byte {
	entry := handler
	switch handler {
	case "vide":
		entry = "avc1"
	case "soun":
		entry = "mp4a"
	case "text":
		entry = "tx3g"
	case "meta":
		entry = "ID3 "
	default:
		entry = "stpp"
	}
	var b boxBuilder
	off := b.start("stsd")
	b.full(0, 0)
	b.u32(1)
	entryOff := b.start(entry)
	if entry == "avc1" {
		b.zeros(6)
		b.u16(1)
		b.u16(0)
		b.u16(0)
		b.zeros(12)
		b.u16(0)
		b.u16(0)
		b.u32(0x00480000)
		b.u32(0x00480000)
		b.u32(0)
		b.u16(1)
		b.zeros(32)
		b.u16(0x0018)
		b.u16(0xffff)
	} else if entry == "mp4a" {
		b.zeros(6)
		b.u16(1)
		b.zeros(8)
		b.u16(2)
		b.u16(16)
		b.u16(0)
		b.u16(0)
		b.u32(48000 << 16)
	} else {
		b.zeros(6)
		b.u16(1)
		if entry == "stpp" {
			b.str("http://www.w3.org/ns/ttml")
			b.u8(0)
			b.u8(0)
			b.u8(0)
		}
	}
	b.finish(entryOff)
	b.finish(off)
	return b.bytes()
}

var _ = mp4.StrToBoxType
