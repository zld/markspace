package index

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"

	"archivist/internal/fault"
)

const (
	mdatHeaderSize = 8
	ftypBrandCount = 3
)

// ftypLength is the byte length of the fixed ftyp box.
func ftypLength() int { return 8 + 8 + 4*ftypBrandCount }

// writeFtyp emits the fixed ftyp box.
func writeFtyp(f *os.File) error {
	payload := make([]byte, 0, 8+4*ftypBrandCount)
	payload = append(payload, 'i', 's', 'o', 'm')
	payload = append(payload, 0, 0, 2, 0)
	for _, brand := range [][4]byte{{'i', 's', 'o', 'm'}, {'i', 's', 'o', '2'}, {'m', 'p', '4', '1'}} {
		payload = append(payload, brand[:]...)
	}
	if _, err := f.Write(boxHeader("ftyp", uint64(8+len(payload)))); err != nil {
		return fault.New(fault.KindIO, fault.StageWrite, "write ftyp header").Wrap(err)
	}
	if _, err := f.Write(payload); err != nil {
		return fault.New(fault.KindIO, fault.StageWrite, "write ftyp payload").Wrap(err)
	}
	return nil
}

// boxHeader frames a small box header of the given total size.
func boxHeader(typ string, size uint64) []byte {
	out := make([]byte, 8)
	if size > 0xFFFFFFFF {
		out[3] = 1
		copy(out[4:], typ)
		return out
	}
	binary.BigEndian.PutUint32(out[:4], uint32(size))
	copy(out[4:], typ)
	return out
}

// memWriteSeeker is an in-memory io.WriteSeeker for assembling moov.
type memWriteSeeker struct {
	buf []byte
	pos int64
}

func (m *memWriteSeeker) Write(p []byte) (int, error) {
	end := m.pos + int64(len(p))
	if end > int64(len(m.buf)) {
		grown := make([]byte, end)
		copy(grown, m.buf)
		m.buf = grown
	}
	copy(m.buf[m.pos:], p)
	m.pos = end
	return len(p), nil
}

func (m *memWriteSeeker) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = m.pos + offset
	case io.SeekEnd:
		abs = int64(len(m.buf)) + offset
	default:
		return 0, fmt.Errorf("index: invalid whence %d", whence)
	}
	if abs < 0 {
		return 0, fmt.Errorf("index: negative seek")
	}
	m.pos = abs
	return abs, nil
}

type boxWriter struct{ w *mp4.Writer }

func (bw *boxWriter) start(typ string) error {
	_, err := bw.w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType(typ)})
	return err
}

func (bw *boxWriter) end() error {
	_, err := bw.w.EndBox()
	return err
}

func (bw *boxWriter) full(typ string, version uint8, flags uint32, payload mp4.IBox) error {
	if err := bw.start(typ); err != nil {
		return err
	}
	payload.SetVersion(version)
	payload.SetFlags(flags)
	if _, err := mp4.Marshal(bw.w, payload, mp4.Context{}); err != nil {
		return err
	}
	return bw.end()
}

// moovLength returns the exact encoded size of moov for these tracks.
func moovLength(tracks []*plannedTrack, movieTS uint32) (int64, error) {
	var buf bytes.Buffer
	if err := writeMoov(&buf, tracks, movieTS); err != nil {
		return 0, err
	}
	return int64(buf.Len()), nil
}

// writeMoov emits a movie header plus one trak per planned track.
func writeMoov(w io.Writer, tracks []*plannedTrack, movieTS uint32) error {
	ws := &memWriteSeeker{}
	bw := &boxWriter{w: mp4.NewWriter(ws)}
	if err := bw.start("moov"); err != nil {
		return wrapWrite("start moov", err)
	}
	if err := writeMvhd(bw, tracks, movieTS); err != nil {
		return err
	}
	for _, t := range tracks {
		if err := writeTrak(bw, t, movieTS); err != nil {
			return err
		}
	}
	if err := bw.end(); err != nil {
		return wrapWrite("finish moov", err)
	}
	_, err := w.Write(ws.buf)
	return err
}

func movieDuration(tracks []*plannedTrack, movieTS uint32) uint64 {
	var max uint64
	for _, t := range tracks {
		d := scaleUint(t.duration, t.timescale, movieTS)
		if d > max {
			max = d
		}
	}
	return max
}

func writeMvhd(bw *boxWriter, tracks []*plannedTrack, movieTS uint32) error {
	mvhd := &mp4.Mvhd{
		Timescale:   movieTS,
		DurationV1:  movieDuration(tracks, movieTS),
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
		NextTrackID: maxTrackID(tracks) + 1,
	}
	if err := bw.full("mvhd", 1, 0, mvhd); err != nil {
		return wrapWrite("write mvhd", err)
	}
	return nil
}

func maxTrackID(tracks []*plannedTrack) uint32 {
	var max uint32
	for _, t := range tracks {
		if t.trackID > max {
			max = t.trackID
		}
	}
	return max
}

func writeTrak(bw *boxWriter, t *plannedTrack, movieTS uint32) error {
	if err := bw.start("trak"); err != nil {
		return wrapWrite("start trak", err)
	}
	tkhd := &mp4.Tkhd{
		TrackID:    t.trackID,
		DurationV1: scaleUint(t.duration, t.timescale, movieTS),
		Matrix:     [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
		Width:      t.width << 16,
		Height:     t.height << 16,
	}
	if err := bw.full("tkhd", 1, 0x000003, tkhd); err != nil {
		return wrapWrite("write tkhd", err)
	}
	if err := bw.start("mdia"); err != nil {
		return wrapWrite("start mdia", err)
	}
	mdhd := &mp4.Mdhd{
		Timescale:  t.timescale,
		DurationV1: t.duration,
		Language:   [3]byte{0x15, 0x0E, 0x04},
	}
	if err := bw.full("mdhd", 1, 0, mdhd); err != nil {
		return wrapWrite("write mdhd", err)
	}
	handler := [4]byte{'v', 'i', 'd', 'e'}
	if t.handler != "" {
		copy(handler[:], t.handler)
	}
	hdlr := &mp4.Hdlr{HandlerType: handler, Name: "ArchivistHandler"}
	if err := bw.full("hdlr", 0, 0, hdlr); err != nil {
		return wrapWrite("write hdlr", err)
	}
	if err := bw.start("minf"); err != nil {
		return wrapWrite("start minf", err)
	}
	if err := bw.full("vmhd", 0, 0x000001, &mp4.Vmhd{}); err != nil {
		return wrapWrite("write vmhd", err)
	}
	if err := bw.start("dinf"); err != nil {
		return wrapWrite("start dinf", err)
	}
	if err := bw.start("dref"); err != nil {
		return wrapWrite("start dref", err)
	}
	if _, err := mp4.Marshal(bw.w, &mp4.Dref{EntryCount: 1}, mp4.Context{}); err != nil {
		return wrapWrite("write dref", err)
	}
	url := &mp4.Url{}
	url.SetFlags(mp4.UrlSelfContained)
	if err := bw.start("url "); err != nil {
		return wrapWrite("start url", err)
	}
	if _, err := mp4.Marshal(bw.w, url, mp4.Context{}); err != nil {
		return wrapWrite("write url", err)
	}
	for i := 0; i < 3; i++ {
		if err := bw.end(); err != nil {
			return wrapWrite("close dref/dinf", err)
		}
	}
	if err := writeStbl(bw, t); err != nil {
		return err
	}
	if err := bw.end(); err != nil {
		return wrapWrite("close minf", err)
	}
	if err := bw.end(); err != nil {
		return wrapWrite("close mdia", err)
	}
	return wrapWrite("close trak", bw.end())
}

func writeStbl(bw *boxWriter, t *plannedTrack) error {
	if err := bw.start("stbl"); err != nil {
		return wrapWrite("start stbl", err)
	}
	if err := bw.start("stsd"); err != nil {
		return wrapWrite("start stsd", err)
	}
	if _, err := mp4.Marshal(bw.w, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return wrapWrite("write stsd", err)
	}
	if err := bw.start("avc1"); err != nil {
		return wrapWrite("start avc1", err)
	}
	entry := &mp4.VisualSampleEntry{
		SampleEntry:     mp4.SampleEntry{AnyTypeBox: mp4.AnyTypeBox{Type: mp4.StrToBoxType("avc1")}, DataReferenceIndex: 1},
		Width:           uint16(t.width),
		Height:          uint16(t.height),
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
		PreDefined3:     -1,
	}
	if _, err := mp4.Marshal(bw.w, entry, mp4.Context{}); err != nil {
		return wrapWrite("write avc1", err)
	}
	if err := bw.end(); err != nil {
		return wrapWrite("close avc1", err)
	}
	if err := bw.end(); err != nil {
		return wrapWrite("close stsd", err)
	}
	stts := &mp4.Stts{Entries: sttsEntries(t.dts)}
	stts.EntryCount = uint32(len(stts.Entries))
	if err := bw.full("stts", 0, 0, stts); err != nil {
		return wrapWrite("write stts", err)
	}
	if len(t.keyframes) != len(t.dts) {
		stss := &mp4.Stss{EntryCount: uint32(len(t.keyframes)), SampleNumber: t.keyframes}
		if err := bw.full("stss", 0, 0, stss); err != nil {
			return wrapWrite("write stss", err)
		}
	}
	stsz := &mp4.Stsz{SampleCount: uint32(len(t.sizes))}
	for _, s := range t.sizes {
		stsz.EntrySize = append(stsz.EntrySize, s)
	}
	if err := bw.full("stsz", 0, 0, stsz); err != nil {
		return wrapWrite("write stsz", err)
	}
	spc := t.samplesPerChunk
	if spc == 0 {
		spc = 1
	}
	stsc := &mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: spc, SampleDescriptionIndex: 1}}}
	if err := bw.full("stsc", 0, 0, stsc); err != nil {
		return wrapWrite("write stsc", err)
	}
	stco := &mp4.Stco{EntryCount: uint32(len(t.chunkOffsets))}
	for _, off := range t.chunkOffsets {
		stco.ChunkOffset = append(stco.ChunkOffset, uint32(off))
	}
	if err := bw.full("stco", 0, 0, stco); err != nil {
		return wrapWrite("write stco", err)
	}
	return wrapWrite("close stbl", bw.end())
}

// sttsEntries run-length encodes decode time deltas. The final sample keeps
// the preceding delta so durations extend past the last timestamp.
func sttsEntries(dts []uint64) []mp4.SttsEntry {
	if len(dts) == 0 {
		return nil
	}
	step := uint64(1)
	if len(dts) > 1 {
		step = dts[1] - dts[0]
	}
	var out []mp4.SttsEntry
	var count uint32
	var cur uint64
	flush := func() {
		if count > 0 {
			out = append(out, mp4.SttsEntry{SampleCount: count, SampleDelta: uint32(cur)})
		}
	}
	for i := 0; i < len(dts); i++ {
		var d uint64
		if i+1 < len(dts) {
			d = dts[i+1] - dts[i]
		} else {
			d = step
		}
		if i == 0 {
			cur, count = d, 1
			continue
		}
		if d == cur {
			count++
			continue
		}
		flush()
		cur, count = d, 1
	}
	flush()
	return out
}

func scaleUint(v uint64, from, to uint32) uint64 {
	if from == 0 || to == 0 || from == to {
		return v
	}
	return (v*uint64(to) + uint64(from)/2) / uint64(from)
}

func wrapWrite(what string, err error) error {
	if err == nil {
		return nil
	}
	return fault.New(fault.KindIO, fault.StageWrite, "%s", what).Wrap(err)
}
