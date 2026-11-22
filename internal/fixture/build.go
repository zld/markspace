package fixture

import (
	"bytes"
	"encoding/binary"
)

// writer emits MP4 boxes and patches sizes on close. It writes structure by
// hand so fixtures can contain boxes that a normal muxer never produces.
type writer struct {
	buf   bytes.Buffer
	stack []frame
}

type frame struct {
	start int
	large bool
}

func newWriter() *writer { return &writer{} }

func (w *writer) open(typ string) {
	start := w.buf.Len()
	w.buf.Write([]byte{0, 0, 0, 0})
	w.buf.WriteString(typ)
	w.stack = append(w.stack, frame{start: start})
}

func (w *writer) openLarge(typ string) {
	start := w.buf.Len()
	w.buf.Write([]byte{0, 0, 0, 1})
	w.buf.WriteString(typ)
	w.buf.Write(make([]byte, 8))
	w.stack = append(w.stack, frame{start: start, large: true})
}

func (w *writer) close() {
	f := w.stack[len(w.stack)-1]
	w.stack = w.stack[:len(w.stack)-1]
	size := w.buf.Len() - f.start
	if f.large {
		binary.BigEndian.PutUint32(w.buf.Bytes()[f.start:], 1)
		binary.BigEndian.PutUint64(w.buf.Bytes()[f.start+8:], uint64(size))
		return
	}
	binary.BigEndian.PutUint32(w.buf.Bytes()[f.start:], uint32(size))
}

func (w *writer) full(typ string, version byte, flags uint32) {
	w.open(typ)
	w.buf.WriteByte(version)
	w.buf.Write([]byte{byte(flags >> 16), byte(flags >> 8), byte(flags)})
}

// box opens typ, runs body, then closes typ. Using it everywhere keeps the
// frame stack balanced; a manual open/close imbalance silently corrupts every
// enclosing size, which is exactly the bug this replaces.
func (w *writer) box(typ string, body func()) {
	w.open(typ)
	body()
	w.close()
}

func (w *writer) fullBox(typ string, version byte, flags uint32, body func()) {
	w.full(typ, version, flags)
	body()
	w.close()
}

func (w *writer) u8(v uint8)   { w.buf.WriteByte(v) }
func (w *writer) u16(v uint16) { var b [2]byte; binary.BigEndian.PutUint16(b[:], v); w.buf.Write(b[:]) }
func (w *writer) u32(v uint32) { var b [4]byte; binary.BigEndian.PutUint32(b[:], v); w.buf.Write(b[:]) }
func (w *writer) zeros(n int)  { w.buf.Write(make([]byte, n)) }
func (w *writer) tag(s string) { w.buf.WriteString(s) }

func fixed1616(v int) uint32 { return uint32(v) << 16 }

func buildFTyp() []byte {
	w := newWriter()
	w.box("ftyp", func() {
		w.tag("M4A ")
		w.u32(0x00000200)
		w.tag("M4A ")
		w.tag("isom")
		w.tag("mp42")
		w.tag("qt  ")
	})
	return w.buf.Bytes()
}

func buildMoov(m *Media, lay *layout, sizes []uint32, offsets []int) []byte {
	w := newWriter()
	w.box("moov", func() {
		writeMvhd(w, m)
		writeTrak(w, m, lay, sizes, offsets)
	})
	return w.buf.Bytes()
}

func writeMvhd(w *writer, m *Media) {
	w.fullBox("mvhd", 0, 0, func() {
		w.u32(0)
		w.u32(0)
		w.u32(m.Timescale)
		w.u32(uint32(m.Frames))
		w.u32(0x00010000)
		w.u16(0x0100)
		w.zeros(2)
		w.zeros(8)
		writeMatrix(w)
		w.zeros(24)
		w.u32(2)
	})
}

func writeMatrix(w *writer) {
	w.u32(0x00010000)
	w.zeros(12)
	w.u32(0x00010000)
	w.zeros(12)
	w.u32(0x40000000)
}

func writeTrak(w *writer, m *Media, lay *layout, sizes []uint32, offsets []int) {
	w.box("trak", func() {
		writeTkhd(w, m)
		w.box("mdia", func() {
			writeMdhd(w, m)
			writeHdlr(w)
			w.box("minf", func() {
				writeSmhd(w)
				writeDinf(w)
				w.box("stbl", func() {
					writeStsd(w, m)
					writeStts(w, len(sizes))
					writeStsc(w, len(sizes), m.Plan.SamplesPerChunk)
					writeStsz(w, sizes)
					writeStco(w, offsets, lay)
				})
			})
		})
	})
}

func writeTkhd(w *writer, m *Media) {
	w.fullBox("tkhd", 0, 0x3, func() {
		w.u32(0)
		w.u32(0)
		w.u32(1)
		w.u32(0)
		w.u32(uint32(m.Frames))
		w.zeros(8)
		w.u16(0)
		w.u16(0)
		w.u16(0x0100)
		w.u16(0)
		writeMatrix(w)
		w.u32(0)
		w.u32(0)
	})
}

func writeMdhd(w *writer, m *Media) {
	w.fullBox("mdhd", 0, 0, func() {
		w.u32(0)
		w.u32(0)
		w.u32(m.Timescale)
		w.u32(uint32(m.Frames))
		w.u16(0x55c4)
		w.u16(0)
	})
}

func writeHdlr(w *writer) {
	w.fullBox("hdlr", 0, 0, func() {
		w.u32(0)
		w.tag("soun")
		w.zeros(12)
		w.tag("SoundHandler")
		w.u8(0)
	})
}

func writeSmhd(w *writer) {
	w.fullBox("smhd", 0, 0, func() {
		w.u16(0)
		w.u16(0)
	})
}

func writeDinf(w *writer) {
	w.box("dinf", func() {
		w.fullBox("dref", 0, 0, func() {
			w.u32(1)
			w.fullBox("url ", 0, 1, func() {})
		})
	})
}

func writeStsd(w *writer, m *Media) {
	w.fullBox("stsd", 0, 0, func() {
		w.u32(1)
		w.box("mp4a", func() {
			w.zeros(6)
			w.u16(1) // data reference index
			w.u16(0) // entry version
			w.zeros(6)
			w.u16(uint16(m.Plan.Channels))
			w.u16(16)
			w.u16(0)
			w.u16(0)
			w.u32(fixed1616(m.Plan.SampleRate))
			writeEsds(w)
		})
	})
}

// writeEsds writes a minimal AAC-LC esds so parsers see the right codec and
// an AudioSpecificConfig carrying the sample rate.
func writeEsds(w *writer) {
	asc := []byte{0x12, 0x10} // AAC-LC, 44100 Hz, stereo
	dcdBody := []byte{0x40, 0x15}
	dcdBody = append(dcdBody, 0, 0, 0, 0, 0, 0, 0)
	dcdBody = append(dcdBody, DecSpecificInfoTag, byte(len(asc)))
	dcdBody = append(dcdBody, asc...)
	sl := []byte{0, 0}
	esBody := []byte{0, 1, 0x04}
	esBody = append(esBody, sizeBytes(len(dcdBody))...)
	esBody = append(esBody, dcdBody...)
	esBody = append(esBody, SLConfigDescrTag, byte(len(sl)))
	esBody = append(esBody, sl...)

	w.fullBox("esds", 0, 0, func() {
		w.u8(ESDescrTag)
		w.buf.Write(sizeBytes(len(esBody)))
		w.buf.Write(esBody)
	})
}

const (
	ESDescrTag         = 0x03
	DecSpecificInfoTag = 0x05
	SLConfigDescrTag   = 0x06
)

func sizeBytes(n int) []byte {
	return []byte{
		byte(0x80 | (n >> 21 & 0x7f)),
		byte(0x80 | (n >> 14 & 0x7f)),
		byte(0x80 | (n >> 7 & 0x7f)),
		byte(n & 0x7f),
	}
}

func writeStts(w *writer, samples int) {
	w.fullBox("stts", 0, 0, func() {
		w.u32(1)
		w.u32(uint32(samples))
		w.u32(1024)
	})
}

func writeStsc(w *writer, samples, per int) {
	if per <= 0 {
		per = 10
	}
	chunks := (samples + per - 1) / per
	full := 0
	if chunks > 1 {
		full = (chunks - 1) * per
	}
	w.fullBox("stsc", 0, 0, func() {
		if chunks <= 1 {
			w.u32(1)
			w.u32(1)
			w.u32(uint32(samples))
			w.u32(1)
			return
		}
		w.u32(2)
		w.u32(1)
		w.u32(uint32(per))
		w.u32(1)
		w.u32(uint32(chunks))
		w.u32(uint32(samples - full))
		w.u32(1)
	})
}

func writeStsz(w *writer, sizes []uint32) {
	w.fullBox("stsz", 0, 0, func() {
		w.u32(0)
		w.u32(uint32(len(sizes)))
		for _, s := range sizes {
			w.u32(s)
		}
	})
}

func writeStco(w *writer, offsets []int, lay *layout) {
	w.fullBox("stco", 0, 0, func() {
		w.u32(uint32(len(offsets)))
		if lay != nil {
			lay.stcoOffsets = make([]int, 0, len(offsets))
		}
		for range offsets {
			if lay != nil {
				lay.stcoOffsets = append(lay.stcoOffsets, w.buf.Len())
			}
			w.u32(0) // patched by patchStco once final offsets are known
		}
	})
}
