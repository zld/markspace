// Package mp4wrap isolates the go-mp4 dependency behind a tiny API used
// by the demo pipeline: write one video track as a minimal MP4 and read
// its sample structure back. Custom AVC/HEVC sample entries that go-mp4
// does not model are emitted through its low-level box writer.
package mp4wrap

import (
	"bytes"
	"encoding/binary"
	"io"

	mp4 "github.com/abema/go-mp4"
)

// Sample is one codec sample placed into the MP4 container.
type Sample struct {
	Data     []byte
	KeyFrame bool
	Duration uint32 // in the container timescale
}

// writeRawBox emits a box header plus an opaque payload for box types
// the high-level go-mp4 marshaller does not know about.
func writeRawBox(w io.Writer, boxType string, payload []byte) error {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr, uint32(8+len(payload)))
	copy(hdr[4:], boxType)
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func marshalBox(w *mp4.Writer, box mp4.IImmutableBox) error {
	_, err := mp4.Marshal(w, box, mp4.Context{})
	return err
}

// Write produces a minimal single-track MP4: ftyp, mdat (all samples in
// one chunk) and moov with stsd/stts/stsc/stsz/stco/stss. The layout is
// fixed so output for identical inputs is byte-stable.
func Write(w io.WriteSeeker, codec string, timescale uint32, samples []Sample) error {
	mw := mp4.NewWriter(w)

	// ftyp
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	if err := marshalBox(mw, &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 0x200,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'i', 's', 'o', '2'}},
		},
	}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}

	// mdat: remember where sample bytes begin for stco.
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return err
	}
	mdatPayloadOffset, err := mw.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	for _, s := range samples {
		if _, err := mw.Write(s.Data); err != nil {
			return err
		}
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}

	// moov
	moov, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()})
	if err != nil {
		return err
	}
	if err := writeMovieHeader(mw, timescale, samples); err != nil {
		return err
	}
	if err := writeTrack(mw, codec, timescale, samples, mdatPayloadOffset); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		_ = moov
		return err
	}
	return nil
}

func writeMovieHeader(mw *mp4.Writer, timescale uint32, samples []Sample) error {
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); err != nil {
		return err
	}
	var total uint64
	for _, s := range samples {
		total += uint64(s.Duration)
	}
	mvhd := &mp4.Mvhd{
		Timescale:  timescale,
		DurationV0: uint32(total),
		Rate:       0x00010000,
		Volume:     0x0100,
		Matrix:     [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
	}
	if err := marshalBox(mw, mvhd); err != nil {
		return err
	}
	_, err := mw.EndBox()
	return err
}

func writeTrack(mw *mp4.Writer, codec string, timescale uint32, samples []Sample, mdatOffset int64) error {
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	// tkhd
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTkhd()}); err != nil {
		return err
	}
	var total uint64
	for _, s := range samples {
		total += uint64(s.Duration)
	}
	tkhd := &mp4.Tkhd{
		TrackID:    1,
		DurationV0: uint32(total),
		Matrix:     [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
		Width:      320 << 16,
		Height:     240 << 16,
	}
	if err := marshalBox(mw, tkhd); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}

	// mdia
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return err
	}
	mdhd := &mp4.Mdhd{Timescale: timescale, DurationV0: uint32(total)}
	if err := marshalBox(mw, mdhd); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeHdlr()}); err != nil {
		return err
	}
	hdlr := &mp4.Hdlr{
		HandlerType: [4]byte{'v', 'i', 'd', 'e'},
		Name:        "mediacheck-video",
	}
	if err := marshalBox(mw, hdlr); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}

	// minf > stbl
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if err := writeStsd(mw, codec); err != nil {
		return err
	}
	if err := writeSampleTables(mw, samples, mdatOffset); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil { // stbl
		return err
	}
	if _, err := mw.EndBox(); err != nil { // minf
		return err
	}
	if _, err := mw.EndBox(); err != nil { // mdia
		return err
	}
	if _, err := mw.EndBox(); err != nil { // trak
		return err
	}
	return nil
}

// writeStsd hand-builds the sample description because go-mp4 has no
// avc1/hvc1 definitions: one visual sample entry plus a minimal codec
// config child box.
func writeStsd(mw *mp4.Writer, codec string) error {
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	entryType := "avc1"
	configType := "avcC"
	configPayload := []byte{
		1, 0x42, 0xC0, 0x1E, 0xFF, 0xE1, 0x00, 0x00, 1, 0x00, 0x00,
	}
	if codec == "H265" {
		entryType = "hvc1"
		configType = "hvcC"
		configPayload = []byte{1, 1, 0x60, 0, 0, 0, 0x80, 0, 0, 0, 0, 0, 0x50, 0, 0, 0}
	}

	entry := &bytes.Buffer{}
	entry.Write(make([]byte, 6))                                  // reserved
	binary.Write(entry, binary.BigEndian, uint16(1))             // data_ref_index
	binary.Write(entry, binary.BigEndian, uint16(0))             // pre_defined
	binary.Write(entry, binary.BigEndian, uint16(0))             // reserved
	entry.Write(make([]byte, 12))                                // pre_defined[3]
	binary.Write(entry, binary.BigEndian, uint16(320))           // width
	binary.Write(entry, binary.BigEndian, uint16(240))           // height
	binary.Write(entry, binary.BigEndian, uint32(0x00480000))    // h resolution
	binary.Write(entry, binary.BigEndian, uint32(0x00480000))    // v resolution
	binary.Write(entry, binary.BigEndian, uint32(0))             // reserved
	binary.Write(entry, binary.BigEndian, uint16(1))             // frame count
	name := []byte("mediacheck")
	compressor := make([]byte, 32)
	compressor[0] = byte(len(name))
	copy(compressor[1:], name)
	entry.Write(compressor)
	binary.Write(entry, binary.BigEndian, uint16(0x0018)) // depth
	binary.Write(entry, binary.BigEndian, uint16(0xFFFF)) // pre_defined

	entryBox := &bytes.Buffer{}
	if err := writeRawBox(entryBox, entryType, entry.Bytes()); err != nil {
		return err
	}
	if err := writeRawBox(entryBox, configType, configPayload); err != nil {
		return err
	}

	payload := &bytes.Buffer{}
	payload.Write([]byte{0, 0, 0, 0}) // fullbox version/flags
	binary.Write(payload, binary.BigEndian, uint32(1))
	payload.Write(entryBox.Bytes())
	if err := writeRawBox(mw, "stsd", payload.Bytes()); err != nil {
		return err
	}
	_, err := mw.EndBox()
	return err
}

func writeSampleTables(mw *mp4.Writer, samples []Sample, mdatOffset int64) error {
	// stts: one run per sample.
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStts()}); err != nil {
		return err
	}
	stts := &mp4.Stts{EntryCount: uint32(len(samples))}
	for _, s := range samples {
		stts.Entries = append(stts.Entries, mp4.SttsEntry{SampleCount: 1, SampleDelta: s.Duration})
	}
	if err := marshalBox(mw, stts); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}

	// stsc: every sample lives in the single chunk.
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsc()}); err != nil {
		return err
	}
	stsc := &mp4.Stsc{
		EntryCount: 1,
		Entries:    []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: uint32(len(samples)), SampleDescriptionIndex: 1}},
	}
	if err := marshalBox(mw, stsc); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}

	// stsz
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsz()}); err != nil {
		return err
	}
	stsz := &mp4.Stsz{SampleCount: uint32(len(samples))}
	for _, s := range samples {
		stsz.EntrySize = append(stsz.EntrySize, uint32(len(s.Data)))
	}
	if err := marshalBox(mw, stsz); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}

	// stco: one chunk at the mdat payload offset.
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStco()}); err != nil {
		return err
	}
	stco := &mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{uint32(mdatOffset)}}
	if err := marshalBox(mw, stco); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}

	// stss: 1-based key-frame sample numbers.
	keys := make([]uint32, 0)
	for i, s := range samples {
		if s.KeyFrame {
			keys = append(keys, uint32(i+1))
		}
	}
	if len(keys) > 0 {
		if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStss()}); err != nil {
			return err
		}
		if err := marshalBox(mw, &mp4.Stss{EntryCount: uint32(len(keys)), SampleNumber: keys}); err != nil {
			return err
		}
		if _, err := mw.EndBox(); err != nil {
			return err
		}
	}
	return nil
}
