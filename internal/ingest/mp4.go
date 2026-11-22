package ingest

import (
	"bytes"
	"encoding/binary"
	"os"

	mp4 "github.com/abema/go-mp4"

	"avcheck/internal/media"
)

// Sample is one entry of a decoded MP4 sample table.
type Sample struct {
	Index    int
	DTS      int64 // decode time in Timescale ticks
	Duration uint32
	Size     uint32
	Key      bool
	Offset   int64 // absolute file offset of sample data
}

// SampleTable is the decoded video sample table of one MP4 track.
type SampleTable struct {
	Timescale uint32
	Samples   []Sample
}

func marshalBox(box mp4.IImmutableBox) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := mp4.Marshal(&buf, box, mp4.Context{}); err != nil {
		return nil, err
	}
	payload := buf.Bytes()
	typ := box.GetType()
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out, uint32(len(out)))
	copy(out[4:], typ[:])
	copy(out[8:], payload)
	return out, nil
}

func container(typ string, children ...[]byte) []byte {
	var buf bytes.Buffer
	var size [4]byte
	total := 8
	for _, c := range children {
		total += len(c)
	}
	binary.BigEndian.PutUint32(size[:], uint32(total))
	buf.Write(size[:])
	buf.WriteString(typ)
	for _, c := range children {
		buf.Write(c)
	}
	return buf.Bytes()
}

func mustMarshal(box mp4.IImmutableBox, err *error) []byte {
	if *err != nil {
		return nil
	}
	b, e := marshalBox(box)
	if e != nil {
		*err = e
	}
	return b
}

// WriteMP4 writes frames as one video track: ftyp, moov with a full sample
// table, then mdat. Frame ticks must strictly increase and deltas must fit
// uint32; violations are rejected as timestamp regressions.
func WriteMP4(path string, timescale uint32, frames []media.Frame) (err error) {
	if len(frames) == 0 || timescale == 0 {
		return media.NewError(media.KindRange, 0, 0, -1, "empty sample set or zero timescale")
	}
	var stts []mp4.SttsEntry
	var stsz, stss []uint32
	var totalSize uint64
	prev := frames[0].Ticks
	for i, f := range frames {
		if i > 0 {
			d := f.Ticks - prev
			if d <= 0 {
				return media.NewError(media.KindRange, f.StreamID, f.Seq, -1,
					"timestamp regression: ticks %d after %d", f.Ticks, prev)
			}
			if d > int64(^uint32(0)) {
				return media.NewError(media.KindOverflow, f.StreamID, f.Seq, -1, "delta %d overflows uint32", d)
			}
			delta := uint32(d)
			if n := len(stts); n > 0 && stts[n-1].SampleDelta == delta {
				stts[n-1].SampleCount++
			} else {
				stts = append(stts, mp4.SttsEntry{SampleCount: 1, SampleDelta: delta})
			}
			prev = f.Ticks
		}
		if len(f.Payload) > int(^uint32(0)) || totalSize+uint64(len(f.Payload)) > uint64(^uint32(0)) {
			return media.NewError(media.KindOverflow, f.StreamID, f.Seq, -1, "sample size overflow")
		}
		stsz = append(stsz, uint32(len(f.Payload)))
		totalSize += uint64(len(f.Payload))
		if f.Key {
			stss = append(stss, uint32(i+1)) // stss is 1-based
		}
	}
	duration := prev - frames[0].Ticks
	var berr error
	ftyp := mustMarshal(&mp4.Ftyp{MajorBrand: [4]byte{'i', 's', 'o', 'm'},
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}}}}, &berr)
	matrix := [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}
	mvhd := mustMarshal(&mp4.Mvhd{Timescale: timescale, DurationV0: uint32(duration),
		Rate: 0x10000, Volume: 0x100, Matrix: matrix, NextTrackID: 2}, &berr)
	tkhd := mustMarshal(&mp4.Tkhd{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 3}},
		TrackID: 1, DurationV0: uint32(duration), Matrix: matrix}, &berr)
	mdhd := mustMarshal(&mp4.Mdhd{Timescale: timescale, DurationV0: uint32(duration),
		Language: [3]byte{'u', 'n', 'd'}}, &berr)
	hdlr := mustMarshal(&mp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: "avcheck"}, &berr)
	vmhd := mustMarshal(&mp4.Vmhd{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 1}}}, &berr)
	url := mustMarshal(&mp4.Url{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 1}}}, &berr)
	dref := mustMarshal(&mp4.Dref{EntryCount: 1}, &berr)
	dinf := container("dinf", append(dref, url...))
	stsd := mustMarshal(&mp4.Stsd{}, &berr)
	sttsB := mustMarshal(&mp4.Stts{EntryCount: uint32(len(stts)), Entries: stts}, &berr)
	stscB := mustMarshal(&mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{
		{FirstChunk: 1, SamplesPerChunk: uint32(len(frames)), SampleDescriptionIndex: 1}}}, &berr)
	stszB := mustMarshal(&mp4.Stsz{SampleCount: uint32(len(frames)), EntrySize: stsz}, &berr)
	stssB := mustMarshal(&mp4.Stss{EntryCount: uint32(len(stss)), SampleNumber: stss}, &berr)
	if berr != nil {
		return media.NewError(media.KindCorrupt, 0, 0, -1, "marshal: %v", berr)
	}
	// stco depends on the moov size; iterate once (size is stable).
	stco := mustMarshal(&mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{0}}, &berr)
	moovLen := 0
	for _, parts := range [][]byte{mvhd, tkhd, mdhd, hdlr, vmhd, dinf, stsd, sttsB, stscB, stszB, stco, stssB} {
		moovLen += len(parts)
	}
	moovLen += 8 * 5 // moov, trak, mdia, minf, stbl container headers
	mdatStart := uint64(len(ftyp)) + uint64(moovLen) + 8
	if mdatStart > uint64(^uint32(0)) {
		return media.NewError(media.KindOverflow, 0, 0, -1, "mdat offset overflow")
	}
	stco = mustMarshal(&mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{uint32(mdatStart)}}, &berr)
	stbl := container("stbl", stsd, sttsB, stscB, stszB, stco, stssB)
	minf := container("minf", vmhd, dinf, stbl)
	mdia := container("mdia", mdhd, hdlr, minf)
	trak := container("trak", tkhd, mdia)
	moov := container("moov", mvhd, trak)
	if len(moov) != moovLen {
		return media.NewError(media.KindState, 0, 0, -1, "moov size drift %d != %d", len(moov), moovLen)
	}
	f, ferr := os.Create(path)
	if ferr != nil {
		return ferr
	}
	defer f.Close()
	if _, err := f.Write(ftyp); err != nil {
		return err
	}
	if _, err := f.Write(moov); err != nil {
		return err
	}
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(8+totalSize))
	copy(hdr[4:], "mdat")
	if _, err := f.Write(hdr[:]); err != nil {
		return err
	}
	for _, fr := range frames {
		if _, err := f.Write(fr.Payload); err != nil {
			return err
		}
	}
	return nil
}

// ReadSampleTable decodes and validates the video sample table of an MP4
// written by WriteMP4. It rejects out-of-range tables, illegal keyframe
// indexes and timestamp regressions.
func ReadSampleTable(path string) (*SampleTable, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	st := &SampleTable{}
	var stts *mp4.Stts
	var stsz *mp4.Stsz
	var stss *mp4.Stss
	var stco *mp4.Stco
	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.Path[len(h.Path)-1] {
		case mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(),
			mp4.BoxTypeMinf(), mp4.BoxTypeStbl():
			return h.Expand()
		}
		if len(h.Path) < 2 || h.Path[0] != mp4.BoxTypeMoov() {
			return nil, nil
		}
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, err
		}
		switch b := box.(type) {
		case *mp4.Mdhd:
			st.Timescale = b.Timescale
		case *mp4.Stts:
			stts = b
		case *mp4.Stsz:
			stsz = b
		case *mp4.Stss:
			stss = b
		case *mp4.Stco:
			stco = b
		}
		return nil, nil
	})
	if err != nil {
		return nil, media.NewError(media.KindCorrupt, 0, 0, -1, "read boxes: %v", err)
	}
	if st.Timescale == 0 || stts == nil || stsz == nil || stco == nil || len(stco.ChunkOffset) == 0 {
		return nil, media.NewError(media.KindCorrupt, 0, 0, -1, "incomplete sample table")
	}
	return buildTable(st, stts, stsz, stss, stco, fi.Size())
}

func buildTable(st *SampleTable, stts *mp4.Stts, stsz *mp4.Stsz, stss *mp4.Stss, stco *mp4.Stco, fileSize int64) (*SampleTable, error) {
	count := int(stsz.SampleCount)
	if count == 0 || len(stsz.EntrySize) < count {
		return nil, media.NewError(media.KindRange, 0, 0, -1,
			"stsz count %d entries %d", count, len(stsz.EntrySize))
	}
	total := uint64(0)
	for _, e := range stts.Entries {
		total += uint64(e.SampleCount)
	}
	if total != uint64(count) {
		return nil, media.NewError(media.KindRange, 0, 0, -1,
			"stts total %d != stsz count %d", total, count)
	}
	key := make([]bool, count)
	if stss != nil {
		prevN := uint32(0)
		for _, n := range stss.SampleNumber {
			if n == 0 || n > uint32(count) || n <= prevN {
				return nil, media.NewError(media.KindRange, 0, uint64(n), -1,
					"illegal keyframe index %d (count %d)", n, count)
			}
			prevN = n
			key[n-1] = true
		}
	}
	base := int64(stco.ChunkOffset[0])
	off := base
	dts := int64(0)
	idx := 0
	for _, e := range stts.Entries {
		for k := uint32(0); k < e.SampleCount; k++ {
			if idx > 0 && e.SampleDelta == 0 {
				return nil, media.NewError(media.KindRange, 0, uint64(idx), -1,
					"timestamp regression: zero delta at sample %d", idx)
			}
			size := int64(stsz.EntrySize[idx])
			if off < 0 || off+size > fileSize {
				return nil, media.NewError(media.KindRange, 0, uint64(idx), off,
					"sample %d [%d,%d) outside file size %d", idx, off, off+size, fileSize)
			}
			st.Samples = append(st.Samples, Sample{
				Index: idx, DTS: dts, Duration: e.SampleDelta,
				Size: stsz.EntrySize[idx], Key: key[idx], Offset: off,
			})
			off += size
			dts += int64(e.SampleDelta)
			idx++
		}
	}
	return st, nil
}

// Times maps sample DTS values onto the shared clock, rejecting regressions.
func (st *SampleTable) Times(c media.Clock) ([]media.Rat, error) {
	out := make([]media.Rat, len(st.Samples))
	for i, s := range st.Samples {
		r, err := c.ToSeconds(s.DTS)
		if err != nil {
			return nil, err
		}
		if i > 0 && r.Cmp(out[i-1]) <= 0 {
			return nil, media.NewError(media.KindRange, 0, uint64(i), -1,
				"sample time regression at %d", i)
		}
		out[i] = r
	}
	return out, nil
}

// KeyIndex builds the seek index from keyframe samples.
func (st *SampleTable) KeyIndex(c media.Clock) (*KeyIndex, error) {
	times, err := st.Times(c)
	if err != nil {
		return nil, err
	}
	var kt []media.Rat
	var ko []int64
	for i, s := range st.Samples {
		if s.Key {
			kt = append(kt, times[i])
			ko = append(ko, int64(i))
		}
	}
	if len(kt) == 0 {
		return nil, media.NewError(media.KindRange, 0, 0, -1, "no keyframes in sample table")
	}
	return NewKeyIndex(kt, ko, len(st.Samples))
}
