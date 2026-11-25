package container

import (
	"errors"
	"io"

	mp4 "github.com/abema/go-mp4"
)

var errSeekOutOfRange = errors.New("seek out of range")

// BuildOptions drives the synthetic MP4 generator. The generator produces a
// single-video-track fragmented layout (ftyp/moov/sidx/mdat) whose declared
// sidx boundaries can be deliberately corrupted for failure fixtures.
type BuildOptions struct {
	Timescale     uint32
	Samples       uint32 // total frames
	SegmentFrames uint32 // sidx reference duration
	StartTick     uint32 // edit-list media start
	Drift         int32  // added to the final sidx duration (boundary drift)
	Truncate      bool   // drop the final sidx reference (truncated layer)
	BadTimebase   uint32 // if non-zero, mdhd timescale is overridden
}

// Build renders an MP4 byte stream from the declared options. It is the
// in-repo fixture source: no external media files are referenced.
func Build(opt BuildOptions) ([]byte, error) {
	ws := &writeSeeker{}
	w := mp4.NewWriter(ws)

	if err := start(w, "ftyp"); err != nil {
		return nil, err
	}
	if err := marshal(w, &mp4.Ftyp{
		MajorBrand:       [4]byte{'i', 's', 'o', 'm'},
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}}},
	}, mp4.Context{}); err != nil {
		return nil, err
	}
	if err := end(w); err != nil {
		return nil, err
	}

	if err := start(w, "moov"); err != nil {
		return nil, err
	}
	if err := start(w, "mvhd"); err != nil {
		return nil, err
	}
	if err := marshal(w, &mp4.Mvhd{Timescale: opt.Timescale, DurationV0: opt.Samples,
		Rate: 0x00010000, Volume: 0x0100, NextTrackID: 2}, mp4.Context{}); err != nil {
		return nil, err
	}
	if err := end(w); err != nil {
		return nil, err
	}
	if err := start(w, "trak"); err != nil {
		return nil, err
	}
	if err := start(w, "tkhd"); err != nil {
		return nil, err
	}
	if err := marshal(w, &mp4.Tkhd{TrackID: 1, DurationV0: opt.Samples}, mp4.Context{}); err != nil {
		return nil, err
	}
	if err := end(w); err != nil {
		return nil, err
	}
	if err := start(w, "mdia"); err != nil {
		return nil, err
	}
	if err := start(w, "mdhd"); err != nil {
		return nil, err
	}
	mdhdScale := opt.Timescale
	if opt.BadTimebase != 0 {
		mdhdScale = opt.BadTimebase
	}
	if err := marshal(w, &mp4.Mdhd{Timescale: mdhdScale, DurationV0: opt.Samples}, mp4.Context{}); err != nil {
		return nil, err
	}
	if err := end(w); err != nil {
		return nil, err
	}
	if err := start(w, "hdlr"); err != nil {
		return nil, err
	}
	if err := marshal(w, &mp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: "packcheck-video"}, mp4.Context{}); err != nil {
		return nil, err
	}
	if err := end(w); err != nil {
		return nil, err
	}
	if err := start(w, "minf"); err != nil {
		return nil, err
	}
	if err := start(w, "stbl"); err != nil {
		return nil, err
	}
	if err := writeSampleTables(w, opt); err != nil {
		return nil, err
	}
	if err := end(w); err != nil { // stbl
		return nil, err
	}
	if err := end(w); err != nil { // minf
		return nil, err
	}
	if opt.StartTick > 0 {
		if err := start(w, "edts"); err != nil {
			return nil, err
		}
		if err := start(w, "elst"); err != nil {
			return nil, err
		}
		if err := marshal(w, &mp4.Elst{EntryCount: 1, Entries: []mp4.ElstEntry{{
			SegmentDurationV0: opt.Samples, MediaTimeV0: int32(opt.StartTick),
			MediaRateInteger: 1,
		}}}, mp4.Context{}); err != nil {
			return nil, err
		}
		if err := end(w); err != nil { // elst
			return nil, err
		}
		if err := end(w); err != nil { // edts
			return nil, err
		}
	}
	if err := end(w); err != nil { // mdia
		return nil, err
	}
	if err := end(w); err != nil { // trak
		return nil, err
	}
	if err := end(w); err != nil { // moov
		return nil, err
	}

	if err := writeSidx(w, opt); err != nil {
		return nil, err
	}
	if err := start(w, "mdat"); err != nil {
		return nil, err
	}
	if _, err := w.Write(make([]byte, opt.Samples)); err != nil {
		return nil, err
	}
	if err := end(w); err != nil {
		return nil, err
	}
	return ws.Bytes(), nil
}

func writeSampleTables(w *mp4.Writer, opt BuildOptions) error {
	if err := start(w, "stsd"); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	if err := end(w); err != nil {
		return err
	}
	if err := start(w, "stts"); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Stts{EntryCount: 1, Entries: []mp4.SttsEntry{{
		SampleCount: opt.Samples, SampleDelta: 1,
	}}}, mp4.Context{}); err != nil {
		return err
	}
	if err := end(w); err != nil {
		return err
	}
	if err := start(w, "stsc"); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{{
		FirstChunk: 1, SamplesPerChunk: opt.Samples,
	}}}, mp4.Context{}); err != nil {
		return err
	}
	if err := end(w); err != nil {
		return err
	}
	if err := start(w, "stsz"); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Stsz{SampleSize: 1, SampleCount: opt.Samples}, mp4.Context{}); err != nil {
		return err
	}
	if err := end(w); err != nil {
		return err
	}
	if err := start(w, "stco"); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{0}}, mp4.Context{}); err != nil {
		return err
	}
	return end(w)
}

func writeSidx(w *mp4.Writer, opt BuildOptions) error {
	var refs []mp4.SidxReference
	for cursor := uint32(0); cursor < opt.Samples; cursor += opt.SegmentFrames {
		dur := opt.SegmentFrames
		if cursor+dur > opt.Samples {
			dur = opt.Samples - cursor
		}
		refs = append(refs, mp4.SidxReference{SubsegmentDuration: dur, ReferencedSize: uint32(dur)})
	}
	if opt.Truncate && len(refs) > 1 {
		refs = refs[:len(refs)-1]
	}
	if opt.Drift != 0 && len(refs) > 0 {
		refs[len(refs)-1].SubsegmentDuration = uint32(int32(refs[len(refs)-1].SubsegmentDuration) + opt.Drift)
	}
	if err := start(w, "sidx"); err != nil {
		return err
	}
	sidx := &mp4.Sidx{
		ReferenceID: 1, Timescale: opt.Timescale, ReferenceCount: uint16(len(refs)),
		References: refs,
	}
	if err := marshal(w, sidx, mp4.Context{}); err != nil {
		return err
	}
	return end(w)
}

func start(w *mp4.Writer, typ string) error {
	_, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType(typ)})
	return err
}

func end(w *mp4.Writer) error {
	_, err := w.EndBox()
	return err
}

func marshal(w io.Writer, box mp4.IImmutableBox, ctx mp4.Context) error {
	_, err := mp4.Marshal(w, box, ctx)
	return err
}

// writeSeeker implements io.WriteSeeker over an extensible byte slice. The
// box writer seeks backwards to rewrite fixed-width size headers, so seeks
// move a cursor while writes place bytes at the cursor and grow the slice
// when appending past the end.
type writeSeeker struct {
	data []byte
	pos  int64
}

func (w *writeSeeker) Write(p []byte) (int, error) {
	if end := w.pos + int64(len(p)); end > int64(len(w.data)) {
		grown := make([]byte, end)
		copy(grown, w.data)
		w.data = grown
	}
	copy(w.data[w.pos:], p)
	w.pos += int64(len(p))
	return len(p), nil
}

func (w *writeSeeker) Seek(offset int64, whence int) (int64, error) {
	pos := w.pos
	switch whence {
	case io.SeekStart:
		pos = offset
	case io.SeekCurrent:
		pos += offset
	case io.SeekEnd:
		pos = int64(len(w.data)) + offset
	}
	if pos < 0 {
		return 0, errSeekOutOfRange
	}
	w.pos = pos
	return pos, nil
}

// Bytes returns the rendered container.
func (w *writeSeeker) Bytes() []byte { return w.data }
