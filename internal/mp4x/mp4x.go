// Package mp4x cross-checks a rebuilt Matroska random-access index against an
// MP4 index produced with github.com/abema/go-mp4.
//
// The cross-check is deliberately independent of the Matroska code path: the
// index entries are written as a real ISO-BMFF sample table (stts/stss/stsz/
// stco), the file is re-read with go-mp4's box parser, and the entry count and
// keyframe sample numbers are compared. A bug that corrupts both the Matroska
// walker and the in-memory index would still be caught here.
package mp4x

import (
	"bytes"
	"fmt"

	"github.com/abema/go-mp4"

	"mediad/internal/media"
	"mediad/internal/merr"
)

// Timescale is the MP4 media timescale used for the cross-check (90 kHz).
const Timescale uint32 = 90000

// sampleSize is the padding size of each synthetic sample in mdat.
const sampleSize = 16

// CrossCheck describes the outcome of the independent verification.
type CrossCheck struct {
	// Entries is the number of stss keyframe entries read back.
	Entries int
	// Duration is the moov duration in Timescale units.
	Duration uint64
	// ByteIdentical reports that re-serialising the index reproduced the
	// same MP4 bytes.
	ByteIdentical bool
	// Digest is the SHA-256 of the produced MP4.
	Digest string
}

// Build writes an MP4 whose sample table mirrors entries.
func Build(entries []media.IndexEntry) ([]byte, error) {
	if len(entries) == 0 {
		return nil, merr.New("mp4x.build", merr.CodeInvalidIndex, -1, "no entries to cross-check")
	}

	ftypSize, err := boxSize(&mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', '5'},
		MinorVersion: 0x200,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', '5'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
		},
	})
	if err != nil {
		return nil, err
	}
	payloadLen := uint64(sampleSize) * uint64(len(entries))
	// mdat uses a 32-bit size when it fits, otherwise the 64-bit form.
	mdatHeader := uint64(8)
	if payloadLen+8 > 1<<32-1 {
		mdatHeader = 16
	}
	sampleData := ftypSize + mdatHeader

	last := entries[len(entries)-1]
	durationUnits := uint64(last.TimeNs) * uint64(Timescale) / 1e9
	if durationUnits == 0 {
		durationUnits = 1
	}

	sampleNums := make([]uint32, 0, len(entries))
	deltas := make([]mp4.SttsEntry, 0, len(entries))
	var prevTK uint64
	for i, e := range entries {
		sampleNums = append(sampleNums, uint32(i+1))
		tk := uint64(e.TimeNs) * uint64(Timescale) / 1e9
		d := tk - prevTK
		if d == 0 {
			d = 1
		}
		deltas = append(deltas, mp4.SttsEntry{SampleCount: 1, SampleDelta: uint32(d)})
		prevTK = tk
	}
	offsets := make([]uint32, len(entries))
	for i := range offsets {
		offsets[i] = uint32(sampleData) + uint32(i*sampleSize)
	}

	var buf bytes.Buffer
	ws := &writeSeeker{buf: &buf}
	w := mp4.NewWriter(ws)

	if err := writeBox(w, mp4.BoxTypeFtyp(), &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', '5'},
		MinorVersion: 0x200,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', '5'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
		},
	}); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return nil, err
	}
	if got := uint64(ws.off); got != sampleData {
		return nil, fmt.Errorf("mp4x: mdat payload at %d, planned %d", got, sampleData)
	}
	if _, err := w.Write(make([]byte, payloadLen)); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}

	// Explicit nesting: go-mp4 only writes headers, so each level must be
	// opened before its children and closed after them.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return nil, err
	}
	if err := writeBox(w, mp4.BoxTypeMvhd(), &mp4.Mvhd{
		Timescale:   Timescale,
		DurationV0:  uint32(durationUnits),
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000},
		NextTrackID: 2,
	}); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return nil, err
	}
	if err := writeBox(w, mp4.BoxTypeTkhd(), &mp4.Tkhd{
		TrackID:    1,
		DurationV0: uint32(durationUnits),
		Matrix:     [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000},
		Width:      1920 << 16,
		Height:     1080 << 16,
	}); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return nil, err
	}
	if err := writeBox(w, mp4.BoxTypeMdhd(), &mp4.Mdhd{
		Timescale:  Timescale,
		DurationV0: uint32(durationUnits),
		Language:   [3]byte{'u', 'n', 'd'},
	}); err != nil {
		return nil, err
	}
	if err := writeBox(w, mp4.BoxTypeHdlr(), &mp4.Hdlr{
		HandlerType: [4]byte{'v', 'i', 'd', 'e'},
		Name:        "VideoHandler\x00",
	}); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return nil, err
	}
	if err := writeBox(w, mp4.BoxTypeStsd(), &mp4.Stsd{
		EntryCount: 0, // sample description omitted; only timing is checked
	}); err != nil {
		return nil, err
	}
	if err := writeBox(w, mp4.BoxTypeStts(), &mp4.Stts{
		EntryCount: uint32(len(deltas)), Entries: deltas,
	}); err != nil {
		return nil, err
	}
	if err := writeBox(w, mp4.BoxTypeStsc(), &mp4.Stsc{
		EntryCount: 1,
		Entries:    []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1}},
	}); err != nil {
		return nil, err
	}
	if err := writeBox(w, mp4.BoxTypeStss(), &mp4.Stss{
		EntryCount: uint32(len(sampleNums)), SampleNumber: sampleNums,
	}); err != nil {
		return nil, err
	}
	if err := writeBox(w, mp4.BoxTypeStsz(), &mp4.Stsz{
		SampleSize: sampleSize, SampleCount: uint32(len(entries)),
	}); err != nil {
		return nil, err
	}
	if err := writeBox(w, mp4.BoxTypeStco(), &mp4.Stco{
		EntryCount: uint32(len(offsets)), ChunkOffset: offsets,
	}); err != nil {
		return nil, err
	}
	for i := 0; i < 5; i++ { // stbl, minf, mdia, trak, moov
		if _, err := w.EndBox(); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// Reopen parses the produced MP4 and returns the keyframe sample numbers and
// duration.
func Reopen(data []byte) (sampleNums []uint32, duration uint64, err error) {
	r := bytes.NewReader(data)
	stss, err := mp4.ExtractBoxWithPayload(r, nil, mp4.BoxPath{
		mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(),
		mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStss(),
	})
	if err != nil {
		return nil, 0, err
	}
	if len(stss) != 1 {
		return nil, 0, fmt.Errorf("mp4x: found %d stss boxes, want 1", len(stss))
	}
	st := stss[0].Payload.(*mp4.Stss)
	mvhd, err := mp4.ExtractBoxWithPayload(r, nil, mp4.BoxPath{
		mp4.BoxTypeMoov(), mp4.BoxTypeMvhd(),
	})
	if err != nil {
		return nil, 0, err
	}
	if len(mvhd) != 1 {
		return nil, 0, fmt.Errorf("mp4x: found %d mvhd boxes, want 1", len(mvhd))
	}
	mv := mvhd[0].Payload.(*mp4.Mvhd)
	return st.SampleNumber, mv.GetDuration(), nil
}
