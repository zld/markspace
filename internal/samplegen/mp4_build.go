package samplegen

import (
	"bytes"
	"encoding/binary"
)

const sampleBytes = 64 // uniform synthetic sample size, matches stsz

// fixed16 encodes an integer into 16.16 fixed point.
func fixed16(v uint32) uint32 { return v << 16 }

// BuildMP4 serialises the spec into a self-contained MP4 byte stream using
// only the documented subset: ftyp/moov(mvhd,trak*(tkhd,mdia(mdhd,hdlr,
// minf(vmhd,dinf,stbl))))/mdat, plus the custom crp0 crop box.
//
// The moov is built once with placeholder zero stco offsets; after measuring
// the final layout each stco value is patched in place, avoiding any
// second-pass buffer aliasing.
func BuildMP4(spec MP4Spec) ([]byte, error) {
	var totalSamples uint32
	for _, t := range spec.Tracks {
		totalSamples += t.SampleN
	}
	mdatPayloadLen := uint64(totalSamples) * sampleBytes

	var ftyp bytes.Buffer
	if err := (boxWriter{&ftyp}).box("ftyp", ftypPayload()); err != nil {
		return nil, err
	}
	moov, err := buildMoov(spec)
	if err != nil {
		return nil, err
	}
	mdatStart := uint64(ftyp.Len()) + uint64(len(moov)) + 8

	var out bytes.Buffer
	out.Write(ftyp.Bytes())
	moovOff := out.Len()
	out.Write(moov)
	if err := (boxWriter{&out}).box("mdat", bytes.Repeat([]byte{0xA5}, int(mdatPayloadLen))); err != nil {
		return nil, err
	}
	data := out.Bytes()

	// Locate each stco offset field by its marker and patch the real offset.
	cursor := mdatStart
	searchFrom := moovOff
	for i := range spec.Tracks {
		idx := bytes.Index(data[searchFrom:], stcoMarker)
		if idx < 0 {
			return nil, errLayoutMismatch
		}
		fieldAt := searchFrom + idx
		off := uint32(cursor)
		if spec.BadSampleRange && i == 0 {
			off = uint32(mdatStart + mdatPayloadLen + 1024)
		}
		binary.BigEndian.PutUint32(data[fieldAt:], off)
		searchFrom = fieldAt + 4
		cursor += uint64(spec.Tracks[i].SampleN) * sampleBytes
	}

	if spec.Truncate {
		at := spec.TruncateAt
		if at <= 0 || at >= len(data) {
			at = len(data) * 6 / 10
		}
		data = append([]byte(nil), data[:at]...)
	}
	return data, nil
}

var errLayoutMismatch = constErr("mp4 layout measurement mismatch")

type constErr string

func (e constErr) Error() string { return string(e) }

func ftypPayload() []byte {
	var b bytes.Buffer
	b.Write([]byte{'i', 's', 'o', 'm'}) // major brand
	b.Write(u32(0x200))                 // minor version
	b.Write([]byte{'i', 's', 'o', 'm'}) // compatible brand
	b.Write([]byte{'a', 'v', 'c', '1'})
	return b.Bytes()
}

// buildMoov serialises the moov box; stco offset fields contain stcoMarker.
func buildMoov(spec MP4Spec) ([]byte, error) {
	var traks bytes.Buffer
	for _, t := range spec.Tracks {
		trak, err := buildTrak(t)
		if err != nil {
			return nil, err
		}
		traks.Write(trak)
	}
	var mvhd bytes.Buffer
	mvhd.Write(fullBox(mvhdPayload(spec)))
	var moov bytes.Buffer
	bw := boxWriter{&moov}
	if err := bw.box("mvhd", mvhd.Bytes()); err != nil {
		return nil, err
	}
	moov.Write(traks.Bytes())
	return boxBytes("moov", moov.Bytes()), nil
}

func mvhdPayload(spec MP4Spec) []byte {
	var b bytes.Buffer
	b.Write(u32(0)) // creation
	b.Write(u32(0)) // modification
	b.Write(u32(1000))
	var maxDur uint32
	for _, t := range spec.Tracks {
		if d := t.SampleN * t.SampleDur * 1000 / t.Timescale; d > maxDur {
			maxDur = d
		}
	}
	b.Write(u32(maxDur))
	b.Write(u32(0x00010000)) // rate 1.0
	b.Write(u16(0x0100))     // volume
	b.Write(u16(0))          // reserved
	b.Write(make([]byte, 8)) // reserved2
	for _, v := range rotationMatrix(0) {
		b.Write(int32Bytes(v))
	}
	for range [6]int{} {
		b.Write(u32(0)) // pre_defined
	}
	b.Write(u32(uint32(len(spec.Tracks)) + 1)) // next track id
	return b.Bytes()
}

func int32Bytes(v int32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(v))
	return b
}

// buildTrak returns the serialised trak box; its stco offset field holds
// stcoMarker and is patched by the caller after the final layout is known.
func buildTrak(t MP4TrackSpec) ([]byte, error) {
	var tkhd bytes.Buffer
	tkhd.Write(fullBox(tkhdPayload(t)))

	var mdhd bytes.Buffer
	mdhd.Write(fullBox(mdhdPayload(t)))

	var hdlr bytes.Buffer
	hdlr.Write(fullBox(hdlrPayload()))

	var vmhd bytes.Buffer
	vmhd.Write(fullBox(append(u16(0), 0, 0, 0, 0, 0, 0))) // graphicsmode + opcolor[3]

	var url bytes.Buffer
	url.Write([]byte{0, 0, 0, 1}) // version 0, flags=1 (self-contained)
	drefBody := fullBox(append(u32(1), boxBytes("url ", url.Bytes())...))
	dinf := boxBytes("dinf", boxBytes("dref", drefBody))

	stbl, err := stblPayload(t)
	if err != nil {
		return nil, err
	}
	var minf bytes.Buffer
	bw := boxWriter{&minf}
	if err := bw.box("vmhd", vmhd.Bytes()); err != nil {
		return nil, err
	}
	minf.Write(dinf)
	if err := bw.box("stbl", stbl); err != nil {
		return nil, err
	}

	var mdia bytes.Buffer
	mw := boxWriter{&mdia}
	if err := mw.box("mdhd", mdhd.Bytes()); err != nil {
		return nil, err
	}
	if err := mw.box("hdlr", hdlr.Bytes()); err != nil {
		return nil, err
	}
	if err := mw.box("minf", minf.Bytes()); err != nil {
		return nil, err
	}

	var inner bytes.Buffer
	tw := boxWriter{&inner}
	if err := tw.box("tkhd", tkhd.Bytes()); err != nil {
		return nil, err
	}
	if err := tw.box("mdia", mdia.Bytes()); err != nil {
		return nil, err
	}
	return boxBytes("trak", inner.Bytes()), nil
}

func tkhdPayload(t MP4TrackSpec) []byte {
	var b bytes.Buffer
	b.Write(u32(0)) // creation
	b.Write(u32(0)) // modification
	b.Write(u32(t.TrackID))
	b.Write(u32(0)) // reserved
	b.Write(u32(t.SampleN * t.SampleDur))
	b.Write(make([]byte, 8)) // reserved1
	b.Write(u16(0))          // layer
	b.Write(u16(0))          // alternate group
	b.Write(u16(0))          // volume
	b.Write(u16(0))          // reserved2
	for _, v := range rotationMatrix(t.Rotation90) {
		b.Write(int32Bytes(v))
	}
	b.Write(u32(fixed16(t.Width)))
	b.Write(u32(fixed16(t.Height)))
	return b.Bytes()
}

func mdhdPayload(t MP4TrackSpec) []byte {
	var b bytes.Buffer
	b.Write(u32(0)) // creation
	b.Write(u32(0)) // modification
	b.Write(u32(t.Timescale))
	b.Write(u32(t.SampleN * t.SampleDur))
	b.Write(u16(0x55C4)) // language 'und'
	b.Write(u16(0))      // pre_defined
	return b.Bytes()
}

func hdlrPayload() []byte {
	var b bytes.Buffer
	b.Write(u32(0))                             // pre_defined
	b.Write([]byte{'v', 'i', 'd', 'e'})         // handler type
	b.Write(make([]byte, 12))                   // reserved
	b.Write([]byte{'v', 'i', 'd', 'e', 'o', 0}) // name
	return b.Bytes()
}
