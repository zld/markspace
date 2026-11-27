package rtpreplay

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/abema/go-mp4"
)

// AudioTrackSpec is the input to the MP4 fixture writer for one track.
type AudioTrackSpec struct {
	Codec       Codec
	Timescale   uint32
	Durations   []uint32 // per sample, in track timescale units
	Sizes       []uint32
	AACASC      []byte // DecoderSpecificInfo for AAC
	OpusPreSkip uint16
	OpusRate    uint32
	Channels    uint16
	SampleRate  uint32
}

// WriteMP4 renders a single-track MP4.
func WriteMP4(spec AudioTrackSpec) ([]byte, error) {
	return WriteMP4Tracks([]AudioTrackSpec{spec})
}

// WriteMP4Tracks renders a minimal but spec-shaped MP4 with one trak per
// spec: ftyp, moov(trak/mdia/minf/stbl with stsd, stts, stsc, stsz, stco), and
// an mdat holding the sample bytes. go-mp4 must read it back via ReadMP4Tracks.
func WriteMP4Tracks(specs []AudioTrackSpec) ([]byte, error) {
	if len(specs) == 0 {
		return nil, NewError(CodeInvalidConfig, "write_mp4", "at least one track is required")
	}
	var mdat []byte
	offsets := make([][]uint32, len(specs))
	for ti, spec := range specs {
		if err := validateSpec(spec); err != nil {
			return nil, err
		}
		offsets[ti] = make([]uint32, len(spec.Sizes))
		for i, n := range spec.Sizes {
			for len(mdat)%4 != 0 {
				mdat = append(mdat, 0)
			}
			offsets[ti][i] = uint32(len(mdat))
			mdat = append(mdat, make([]byte, n)...)
		}
	}
	buf := &bytes.Buffer{}
	w := &mp4ChunkWriter{buf: buf}
	writeFtyp(w, specs[0].Codec)
	startBox(w, "moov")
	writeMvhd(w, specs[0].Timescale, totalDuration(specs[0]))
	// Record where each track's stco entry table lands so the offsets can be
	// patched per track after the mdat position is known.
	stcoPos := make([]int, len(specs))
	for ti, spec := range specs {
		stcoPos[ti] = writeTrak(w, spec, uint32(ti+1), len(offsets[ti]))
	}
	endBox(w)

	mdatPayloadStart := uint32(w.pos()) + 8
	startBox(w, "mdat")
	w.raw(mdat)
	endBox(w)
	out := buf.Bytes()
	for ti, trackOffsets := range offsets {
		for i, rel := range trackOffsets {
			binary.BigEndian.PutUint32(out[stcoPos[ti]+4*i:], mdatPayloadStart+rel)
		}
	}
	return out, nil
}

func validateSpec(spec AudioTrackSpec) error {
	if spec.Timescale == 0 || len(spec.Durations) == 0 || len(spec.Sizes) == 0 {
		return NewError(CodeInvalidConfig, "write_mp4", "timescale, durations and sizes are required")
	}
	if len(spec.Durations) != len(spec.Sizes) {
		return NewError(CodeInvalidConfig, "write_mp4", "durations and sizes length mismatch")
	}
	total := totalDuration(spec)
	if total > 0xFFFFFFFF {
		return NewError(CodeInvalidConfig, "write_mp4", "fixture duration exceeds v0 box limits")
	}
	return nil
}

func totalDuration(spec AudioTrackSpec) uint64 {
	var dur uint64
	for _, d := range spec.Durations {
		dur += uint64(d)
	}
	return dur
}

// writeTrak writes one track and returns the byte offset of its first stco
// chunk-offset entry, which the caller patches once mdat is placed.
func writeTrak(w *mp4ChunkWriter, spec AudioTrackSpec, trackID uint32, sampleCount int) int {
	dur := totalDuration(spec)
	startBox(w, "trak")
	writeTkhd(w, trackID, dur)
	startBox(w, "mdia")
	writeMdhd(w, spec.Timescale, dur)
	writeHdlr(w, "soun")
	startBox(w, "minf")
	writeSmhd(w)
	startBox(w, "dinf")
	startBox(w, "dref")
	writeFullBoxHeader(w, 0, 0, []byte{0, 0, 0, 1})
	startBox(w, "url ")
	writeFullBoxHeader(w, 0, 1, nil)
	endBox(w)
	endBox(w)
	endBox(w)
	startBox(w, "stbl")
	writeStsd(w, spec)
	writeStts(w, spec.Durations)
	writeStsc(w)
	writeStsz(w, spec.Sizes)
	startBox(w, "stco")
	writeFullBoxHeader(w, 0, 0, nil)
	w.u32(uint32(sampleCount))
	entryPos := w.pos()
	for i := 0; i < sampleCount; i++ {
		w.u32(0) // patched after mdat placement
	}
	endBox(w)
	endBox(w)
	endBox(w)
	endBox(w)
	endBox(w)
	return entryPos
}

// mp4ChunkWriter is a byte-oriented box writer. Sizes are patched at EndBox.
type mp4ChunkWriter struct {
	buf   *bytes.Buffer
	stack []int
}

func (w *mp4ChunkWriter) pos() int { return w.buf.Len() }

func (w *mp4ChunkWriter) u8(v uint8)   { w.buf.WriteByte(v) }
func (w *mp4ChunkWriter) u16(v uint16) { _ = binary.Write(w.buf, binary.BigEndian, v) }
func (w *mp4ChunkWriter) u32(v uint32) { _ = binary.Write(w.buf, binary.BigEndian, v) }
func (w *mp4ChunkWriter) u64(v uint64) { _ = binary.Write(w.buf, binary.BigEndian, v) }
func (w *mp4ChunkWriter) raw(b []byte) { w.buf.Write(b) }
func (w *mp4ChunkWriter) bytes4(s string) {
	if len(s) != 4 {
		panic(fmt.Sprintf("box type %q must be 4 bytes", s))
	}
	w.buf.WriteString(s)
}

func startBox(w *mp4ChunkWriter, typ string) {
	w.stack = append(w.stack, w.pos())
	w.u32(0)
	w.bytes4(typ)
}

func endBox(w *mp4ChunkWriter) {
	start := w.stack[len(w.stack)-1]
	w.stack = w.stack[:len(w.stack)-1]
	binary.BigEndian.PutUint32(w.buf.Bytes()[start:], uint32(w.pos()-start))
}

func writeFullBoxHeader(w *mp4ChunkWriter, version uint8, flags uint32, extra []byte) {
	w.u8(version)
	w.u8(uint8(flags >> 16))
	w.u8(uint8(flags >> 8))
	w.u8(uint8(flags))
	if extra != nil {
		w.raw(extra)
	}
}

func writeFtyp(w *mp4ChunkWriter, codec Codec) {
	startBox(w, "ftyp")
	brand := "M4A "
	compat := []string{"M4A ", "mp42", "isom"}
	if codec == CodecOpus {
		brand = "iso5"
		compat = []string{"iso5", "mp41", "Opus"}
	}
	w.bytes4(brand)
	w.u32(0)
	for _, c := range compat {
		w.bytes4(c)
	}
	endBox(w)
}

func writeMvhd(w *mp4ChunkWriter, timescale uint32, duration uint64) {
	startBox(w, "mvhd")
	writeFullBoxHeader(w, 0, 0, nil)
	w.u32(0) // creation
	w.u32(0) // modification
	w.u32(timescale)
	if duration > 0xFFFFFFFF {
		panic("fixture duration exceeds v0 mvhd; use short fixtures")
	}
	w.u32(uint32(duration))
	w.u32(0x00010000) // rate 1.0
	w.u16(0x0100)     // volume 1.0
	w.u16(0)
	w.u32(0)
	w.u32(0)
	for _, v := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
		w.u32(v)
	}
	for i := 0; i < 6; i++ {
		w.u32(0)
	}
	w.u32(2) // next track ID
	endBox(w)
}

func writeTkhd(w *mp4ChunkWriter, trackID uint32, duration uint64) {
	startBox(w, "tkhd")
	writeFullBoxHeader(w, 0, 0x000007, nil) // enabled|in_movie|in_preview
	w.u32(0)
	w.u32(0)
	w.u32(trackID)
	w.u32(0)
	w.u32(uint32(duration))
	w.u32(0)
	w.u32(0)
	w.u16(0)      // layer
	w.u16(0)      // alternate group
	w.u16(0x0100) // volume
	w.u16(0)
	for _, v := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
		w.u32(v)
	}
	w.u32(0)
	w.u32(0)
	endBox(w)
}

func writeMdhd(w *mp4ChunkWriter, timescale uint32, duration uint64) {
	startBox(w, "mdhd")
	writeFullBoxHeader(w, 0, 0, nil)
	w.u32(0)
	w.u32(0)
	w.u32(timescale)
	w.u32(uint32(duration))
	w.u16(0x55C4) // packed ISO-639-2/T "und"
	w.u16(0)
	endBox(w)
}

func writeHdlr(w *mp4ChunkWriter, handler string) {
	startBox(w, "hdlr")
	writeFullBoxHeader(w, 0, 0, nil)
	w.u32(0)
	w.bytes4(handler)
	w.u32(0)
	w.u32(0)
	w.u32(0)
	w.u8(0) // empty name (C string terminator)
	endBox(w)
}

func writeSmhd(w *mp4ChunkWriter) {
	startBox(w, "smhd")
	writeFullBoxHeader(w, 0, 0, nil)
	w.u16(0)
	w.u16(0)
	endBox(w)
}

func writeStsd(w *mp4ChunkWriter, spec AudioTrackSpec) {
	startBox(w, "stsd")
	writeFullBoxHeader(w, 0, 0, nil)
	w.u32(1)
	if spec.Codec == CodecOpus {
		startBox(w, "Opus")
	} else {
		startBox(w, "mp4a")
	}
	// AudioSampleEntry
	w.raw(make([]byte, 6))
	w.u16(1) // data reference index
	w.u16(0) // entry version
	w.u16(0) // reserved[0]
	w.u16(0) // reserved[1]
	w.u16(0) // reserved[2]
	if spec.Channels == 0 {
		spec.Channels = 2
	}
	if spec.SampleRate == 0 {
		spec.SampleRate = 48000
	}
	w.u16(spec.Channels)
	w.u16(16) // sample size
	w.u16(0)
	w.u16(0)
	w.u32(spec.SampleRate << 16)
	if spec.Codec == CodecOpus {
		startBox(w, "dOps")
		w.u8(0)
		w.u8(uint8(spec.Channels))
		w.u16(spec.OpusPreSkip)
		if spec.OpusRate == 0 {
			spec.OpusRate = 48000
		}
		w.u32(spec.OpusRate)
		w.u16(0) // gain
		w.u8(0)  // mapping family
		endBox(w)
	} else {
		writeEsds(w, spec.AACASC, spec.SampleRate)
	}
	endBox(w) // sample entry
	endBox(w) // stsd
}

// writeEsds emits an esds box with ES, DecoderConfig and DecoderSpecificInfo
// descriptors. Sizes use the single-byte form, which fixtures stay under.
func writeEsds(w *mp4ChunkWriter, asc []byte, sampleRate uint32) {
	startBox(w, "esds")
	writeFullBoxHeader(w, 0, 0, nil)
	// ES_Descriptor (0x03)
	es := &bytes.Buffer{}
	es.WriteByte(0x00)
	es.WriteByte(0x01) // ES ID
	es.WriteByte(0)    // flags
	// DecoderConfigDescriptor (0x04)
	dc := &bytes.Buffer{}
	dc.WriteByte(0x40) // Audio ISO/IEC 14496-3
	dc.WriteByte(0x15) // streamType=5 (audio), upstream=0, reserved=1
	dc.Write([]byte{0, 0, 0})
	dc.Write([]byte{0, 0, 0, 0})
	dc.Write([]byte{0, 0, 0, 0})
	// DecoderSpecificInfo (0x05)
	if len(asc) > 0 {
		dc.WriteByte(0x05)
		dc.WriteByte(byte(len(asc)))
		dc.Write(asc)
	}
	// SLConfigDescriptor (0x06)
	dc.WriteByte(0x06)
	dc.WriteByte(1)
	dc.WriteByte(0x02)
	writeDescriptor(es, 0x04, dc.Bytes())
	_ = sampleRate
	writeDescriptorRaw(w, 0x03, es.Bytes())
	endBox(w)
}

func writeDescriptor(w *bytes.Buffer, tag byte, payload []byte) {
	w.WriteByte(tag)
	writeVarint(w, len(payload))
	w.Write(payload)
}

func writeDescriptorRaw(w *mp4ChunkWriter, tag byte, payload []byte) {
	w.u8(tag)
	// varint size, 4-byte form is legal for any length
	w.u8(0x80 | byte(len(payload)>>21))
	w.u8(0x80 | byte(len(payload)>>14))
	w.u8(0x80 | byte(len(payload)>>7))
	w.u8(byte(len(payload)))
	w.raw(payload)
}

func writeVarint(w *bytes.Buffer, n int) {
	w.WriteByte(0x80 | byte(n>>21))
	w.WriteByte(0x80 | byte(n>>14))
	w.WriteByte(0x80 | byte(n>>7))
	w.WriteByte(byte(n))
}

func writeStts(w *mp4ChunkWriter, durations []uint32) {
	startBox(w, "stts")
	writeFullBoxHeader(w, 0, 0, nil)
	type run struct {
		count, delta uint32
	}
	var runs []run
	for _, d := range durations {
		if n := len(runs); n > 0 && runs[n-1].delta == d {
			runs[n-1].count++
			continue
		}
		runs = append(runs, run{count: 1, delta: d})
	}
	w.u32(uint32(len(runs)))
	for _, r := range runs {
		w.u32(r.count)
		w.u32(r.delta)
	}
	endBox(w)
}

func writeStsc(w *mp4ChunkWriter) {
	startBox(w, "stsc")
	writeFullBoxHeader(w, 0, 0, nil)
	w.u32(1)
	w.u32(1) // first chunk
	w.u32(1) // samples per chunk
	w.u32(1) // sample description index
	endBox(w)
}

func writeStsz(w *mp4ChunkWriter, sizes []uint32) {
	startBox(w, "stsz")
	writeFullBoxHeader(w, 0, 0, nil)
	uniform := true
	for _, s := range sizes {
		if s != sizes[0] {
			uniform = false
			break
		}
	}
	if uniform {
		w.u32(sizes[0])
		w.u32(uint32(len(sizes)))
	} else {
		w.u32(0)
		w.u32(uint32(len(sizes)))
		for _, s := range sizes {
			w.u32(s)
		}
	}
	endBox(w)
}

var _ = mp4.BoxTypeMdat
