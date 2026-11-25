// Package mp4 remuxes lossless PCM track data into self-contained MP4
// segments and re-parses every output with go-mp4. The muxer performs no
// resampling or lossy encoding: sample bytes are copied verbatim into mdat
// and re-read through the stbl index tables for byte-level verification.
package mp4

import (
	"bytes"
	"io"

	gomp4 "github.com/abema/go-mp4"

	"mediad/internal/clock"
	"mediad/internal/fault"
)

// TrackSpec describes one mono PCM target track.
type TrackSpec struct {
	TrackID       uint32
	BitsPerSample uint16
	SampleRate    uint32
}

// Segment is one muxing unit: every track contributes frames sample frames
// of PCM; the segment covers the half-open FrameRange.
type Segment struct {
	Frames     clock.FrameRange
	Tracks     []TrackSpec
	PCM        [][]byte // per-track mono PCM, parallel to Tracks
	KeyframeAt uint64   // absolute frame number flagged as a keyframe
}

// MuxWrite writes one segment as a standalone MP4 to w. The file layout is
// ftyp, mdat, moov so chunk offsets are known before moov is marshaled.
func MuxWrite(w io.WriteSeeker, seg Segment) error {
	if len(seg.Tracks) == 0 || len(seg.Tracks) != len(seg.PCM) {
		return fault.New(fault.CatWrite, "mp4.mux", true,
			"segment track/PCM arrays must be non-empty and parallel")
	}
	mw := gomp4.NewWriter(w)
	if err := writeFtyp(mw); err != nil {
		return err
	}
	// mdat: concatenate per-track PCM, one chunk per track.
	trackOffsets := make([]uint64, len(seg.Tracks))
	mdatStart, err := mw.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMdat()})
	if err != nil {
		return wrapWrite(err)
	}
	var mdatPayload bytes.Buffer
	for i, pcm := range seg.PCM {
		trackOffsets[i] = mdatStart.Offset + mdatStart.HeaderSize + uint64(mdatPayload.Len())
		if _, err := mdatPayload.Write(pcm); err != nil {
			return wrapWrite(err)
		}
	}
	if _, err := mw.Write(mdatPayload.Bytes()); err != nil {
		return wrapWrite(err)
	}
	if _, err := mw.EndBox(); err != nil {
		return wrapWrite(err)
	}
	if err := writeMoov(mw, seg, trackOffsets); err != nil {
		return err
	}
	return nil
}

func writeFtyp(mw *gomp4.Writer) error {
	if _, err := mw.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeFtyp()}); err != nil {
		return wrapWrite(err)
	}
	ftyp := &gomp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 0,
		CompatibleBrands: []gomp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
		},
	}
	if _, err := gomp4.Marshal(mw, ftyp, gomp4.Context{}); err != nil {
		return wrapWrite(err)
	}
	if _, err := mw.EndBox(); err != nil {
		return wrapWrite(err)
	}
	return nil
}

func writeMoov(mw *gomp4.Writer, seg Segment, offsets []uint64) error {
	if _, err := mw.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMoov()}); err != nil {
		return wrapWrite(err)
	}
	totalFrames := seg.Frames.Len()
	if err := marshalBox(mw, gomp4.BoxTypeMvhd(), &gomp4.Mvhd{
		Timescale:   seg.Tracks[0].SampleRate,
		DurationV0:  uint32(totalFrames),
		Rate:        0x00010000,
		Volume:      0x0100,
		NextTrackID: uint32(len(seg.Tracks)) + 1,
		Matrix:      identityMatrix(),
	}); err != nil {
		return err
	}
	for i, tr := range seg.Tracks {
		if err := writeTrak(mw, tr, seg, i, offsets[i]); err != nil {
			return err
		}
	}
	if _, err := mw.EndBox(); err != nil {
		return wrapWrite(err)
	}
	return nil
}

func writeTrak(mw *gomp4.Writer, tr TrackSpec, seg Segment, idx int, offset uint64) error {
	if _, err := mw.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeTrak()}); err != nil {
		return wrapWrite(err)
	}
	frames := seg.Frames.Len()
	if err := marshalBox(mw, gomp4.BoxTypeTkhd(), &gomp4.Tkhd{
		TrackID:    tr.TrackID,
		DurationV0: uint32(frames),
		Volume:     0x0100,
		Matrix:     identityMatrix(),
	}); err != nil {
		return err
	}
	if _, err := mw.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMdia()}); err != nil {
		return wrapWrite(err)
	}
	if err := marshalBox(mw, gomp4.BoxTypeMdhd(), &gomp4.Mdhd{
		Timescale:  tr.SampleRate,
		DurationV0: uint32(frames),
	}); err != nil {
		return err
	}
	if err := marshalBox(mw, gomp4.BoxTypeHdlr(), &gomp4.Hdlr{
		HandlerType: [4]byte{'s', 'o', 'u', 'n'},
		Name:        "pcm",
	}); err != nil {
		return err
	}
	if err := writeMinf(mw, tr, seg, idx, offset); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil { // mdia
		return wrapWrite(err)
	}
	if _, err := mw.EndBox(); err != nil { // trak
		return wrapWrite(err)
	}
	return nil
}

func writeMinf(mw *gomp4.Writer, tr TrackSpec, seg Segment, idx int, offset uint64) error {
	if _, err := mw.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMinf()}); err != nil {
		return wrapWrite(err)
	}
	if err := marshalBox(mw, gomp4.BoxTypeSmhd(), &gomp4.Smhd{}); err != nil {
		return err
	}
	// dinf/dref with a self-contained URL entry.
	if _, err := mw.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeDinf()}); err != nil {
		return wrapWrite(err)
	}
	if _, err := mw.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeDref()}); err != nil {
		return wrapWrite(err)
	}
	if _, err := gomp4.Marshal(mw, &gomp4.Dref{EntryCount: 1}, gomp4.Context{}); err != nil {
		return wrapWrite(err)
	}
	url := &gomp4.Url{FullBox: gomp4.FullBox{Flags: [3]byte{0x00, 0x00, 0x01}}}
	if err := marshalBox(mw, gomp4.BoxTypeUrl(), url); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil { // dref
		return wrapWrite(err)
	}
	if _, err := mw.EndBox(); err != nil { // dinf
		return wrapWrite(err)
	}
	if err := writeStbl(mw, tr, seg, idx, offset); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil { // minf
		return wrapWrite(err)
	}
	return nil
}

// writeStbl emits one sample per PCM frame; PCM means every sample has a
// constant delta of one timescale unit.
func writeStbl(mw *gomp4.Writer, tr TrackSpec, seg Segment, idx int, offset uint64) error {
	if _, err := mw.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeStbl()}); err != nil {
		return wrapWrite(err)
	}
	// stsd contains exactly one mp4a audio sample entry.
	if _, err := mw.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeStsd()}); err != nil {
		return wrapWrite(err)
	}
	if _, err := gomp4.Marshal(mw, &gomp4.Stsd{EntryCount: 1}, gomp4.Context{}); err != nil {
		return wrapWrite(err)
	}
	// AudioSampleEntry is an any-type box: constructing it literally would
	// leave its dynamic box type zero, so ask the library to instantiate one
	// bound to the mp4a type.
	box, err := gomp4.BoxTypeMp4a().New(gomp4.Context{})
	if err != nil {
		return wrapWrite(err)
	}
	mp4a := box.(*gomp4.AudioSampleEntry)
	// Assign individual fields: overwriting the embedded SampleEntry struct
	// wholesale would also zero the AnyTypeBox that carries the mp4a type.
	mp4a.DataReferenceIndex = 1
	mp4a.ChannelCount = 1
	mp4a.SampleSize = tr.BitsPerSample
	mp4a.SampleRate = tr.SampleRate << 16 // fixed-point 16.16
	if err := marshalBox(mw, gomp4.BoxTypeMp4a(), mp4a); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil { // stsd
		return wrapWrite(err)
	}
	frames := seg.Frames.Len()
	if err := marshalBox(mw, gomp4.BoxTypeStts(), &gomp4.Stts{
		EntryCount: 1,
		Entries:    []gomp4.SttsEntry{{SampleCount: uint32(frames), SampleDelta: 1}},
	}); err != nil {
		return err
	}
	if err := marshalBox(mw, gomp4.BoxTypeStsc(), &gomp4.Stsc{
		EntryCount: 1,
		Entries: []gomp4.StscEntry{{
			FirstChunk: 1, SamplesPerChunk: uint32(frames), SampleDescriptionIndex: 1,
		}},
	}); err != nil {
		return err
	}
	bytesPerSample := uint32(tr.BitsPerSample) / 8
	if err := marshalBox(mw, gomp4.BoxTypeStsz(), &gomp4.Stsz{
		SampleSize:  bytesPerSample,
		SampleCount: uint32(frames),
	}); err != nil {
		return err
	}
	if err := marshalBox(mw, gomp4.BoxTypeStco(), &gomp4.Stco{
		EntryCount:  1,
		ChunkOffset: []uint32{uint32(offset)},
	}); err != nil {
		return err
	}
	// Every segment start is a random-access keyframe for PCM.
	if err := marshalBox(mw, gomp4.BoxTypeStss(), &gomp4.Stss{
		EntryCount:   1,
		SampleNumber: []uint32{1},
	}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil { // stbl
		return wrapWrite(err)
	}
	return nil
}

func marshalBox(mw *gomp4.Writer, boxType gomp4.BoxType, payload gomp4.IImmutableBox) error {
	if _, err := mw.StartBox(&gomp4.BoxInfo{Type: boxType}); err != nil {
		return wrapWrite(err)
	}
	if _, err := gomp4.Marshal(mw, payload, gomp4.Context{}); err != nil {
		return fault.Wrap(fault.CatWrite, "mp4.marshal:"+boxType.String(), -1, -1, true, err)
	}
	if _, err := mw.EndBox(); err != nil {
		return wrapWrite(err)
	}
	return nil
}

func identityMatrix() [9]int32 {
	return [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
}

func wrapWrite(err error) error {
	if fe, ok := fault.As(err); ok {
		return fe
	}
	return fault.Wrap(fault.CatWrite, "mp4.write", -1, -1, true, err)
}
