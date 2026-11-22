package index

import (
	"encoding/binary"
	"errors"
	"io"

	"github.com/abema/go-mp4"
)

// errSeekRewrite is returned if the in-memory writer is asked to seek to a
// position other than the end; the box writer only rewrites sizes, which the
// growable buffer supports via write-at.
var (
	errSeekRewrite = errors.New("index: seek outside supported range")
	errNoStco      = errors.New("index: missing or inconsistent stco after write")
)

// offsetPlace records where one track's samples begin and how many there are.
type offsetPlace struct {
	stcoPayload int64
	count       int64
}

// WriteTrack is one self-contained track description for serialization.
// SampleData holds the concatenated sample payloads in sample order.
type WriteTrack struct {
	TrackID    uint32
	Timescale  uint32
	IsVideo    bool
	Width      uint16 // pixels (VisualSampleEntry stores uint16)
	Height     uint16
	Volume     uint16 // 0x0100 for audio
	Deltas     []uint32
	Sizes      []uint32
	Keyframes  []bool
	SampleData []byte
}

// memSeeker is a growable byte buffer supporting write-at, read-at and seek
// to any already-written position. The MP4 writer relies on backward seeks to
// patch nested box headers, which this buffer supports directly.
type memSeeker struct {
	buf []byte
	pos int64
}

func (m *memSeeker) Write(p []byte) (int, error) {
	if end := int(m.pos) + len(p); end > len(m.buf) {
		m.buf = append(m.buf, make([]byte, end-len(m.buf))...)
	}
	copy(m.buf[m.pos:], p)
	m.pos += int64(len(p))
	return len(p), nil
}

func (m *memSeeker) Read(p []byte) (int, error) {
	if m.pos >= int64(len(m.buf)) {
		return 0, io.EOF
	}
	n := copy(p, m.buf[m.pos:])
	m.pos += int64(n)
	return n, nil
}

func (m *memSeeker) Seek(offset int64, whence int) (int64, error) {
	var pos int64
	switch whence {
	case io.SeekStart:
		pos = offset
	case io.SeekCurrent:
		pos = m.pos + offset
	case io.SeekEnd:
		pos = int64(len(m.buf)) + offset
	}
	if pos < 0 || pos > int64(len(m.buf)) {
		return 0, errSeekRewrite
	}
	m.pos = pos
	return pos, nil
}

// WriteMP4 serializes a deterministic, single-pass MP4 container. One chunk
// per sample keeps stsc trivial; stco offsets are patched to the real mdat
// payload position after all box sizes are known.
func WriteMP4(tracks []WriteTrack) ([]byte, error) {
	mem := &memSeeker{}
	w := mp4.NewWriter(mem)

	if err := box(w, mp4.BoxTypeFtyp(), &mp4.Ftyp{
		MajorBrand: [4]byte{'i', 's', 'o', 'm'},
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
		},
	}); err != nil {
		return nil, err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return nil, err
	}
	if err := box(w, mp4.BoxTypeMvhd(),
		&mp4.Mvhd{Timescale: 1000, DurationV0: 1, Rate: 0x00010000, Volume: 0x0100,
			Matrix: identityMatrix()}); err != nil {
		return nil, err
	}
	for i := range tracks {
		if err := writeTrak(w, tracks[i]); err != nil {
			return nil, err
		}
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return nil, err
	}
	mdatPayload := int64(len(mem.buf))
	// Record each sample's absolute offset while appending payloads.
	places := make([]offsetPlace, len(tracks))
	running := mdatPayload
	for i := range tracks {
		places[i] = offsetPlace{stcoPayload: running, count: int64(len(tracks[i].Sizes))}
		if _, err := w.Write(tracks[i].SampleData); err != nil {
			return nil, err
		}
		running += int64(len(tracks[i].SampleData))
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}

	if err := patchOffsets(mem.buf, tracks, places); err != nil {
		return nil, err
	}
	return mem.buf, nil
}

// stcoLoc records one stco box's table position and entry count, located by a
// pure byte scan so zero placeholder offsets never trigger payload parsing.
type stcoLoc struct {
	tablePos int64 // absolute byte offset of the first stco entry
	count    int
}

// patchOffsets byte-walks the finished container, finds every stco box and
// rewrites its entries to the sample's absolute byte offset inside mdat. The
// walk only reads box headers (8 bytes), never sample payloads, so it is
// immune to the zero placeholders written in the first pass.
func patchOffsets(raw []byte, tracks []WriteTrack, places []offsetPlace) error {
	locs, err := scanStco(raw)
	if err != nil {
		return err
	}
	if len(locs) != len(tracks) {
		return errNoStco
	}
	for ti, loc := range locs {
		if loc.count != len(tracks[ti].Sizes) {
			return errNoStco
		}
		off := places[ti].stcoPayload
		for i := 0; i < loc.count; i++ {
			pos := loc.tablePos + int64(i*4)
			if pos+4 > int64(len(raw)) {
				return errNoStco
			}
			binary.BigEndian.PutUint32(raw[pos:], uint32(off))
			off += int64(tracks[ti].Sizes[i])
		}
	}
	return nil
}

// scanStco recursively walks only structural container boxes. Sample-entry
// (avc1/mp4a) and data-reference (url) items carry their type inside the
// parent payload rather than as standalone boxes, so the walk never descends
// into stsd or dref; it follows moov/trak/mdia/minf/stbl only.
func scanStco(raw []byte) ([]stcoLoc, error) {
	var out []stcoLoc
	var walk func(start, end int64) error
	walk = func(start, end int64) error {
		p := start
		for p+8 <= end {
			size := int64(binary.BigEndian.Uint32(raw[p : p+4]))
			typ := string(raw[p+4 : p+8])
			if size < 8 || p+size > end {
				return errNoStco
			}
			switch typ {
			case "stco":
				// Box header(8) + version/flags(4) + entry_count(4), then
				// one uint32 absolute offset per sample entry.
				if p+16 > end {
					return errNoStco
				}
				count := int(binary.BigEndian.Uint32(raw[p+12 : p+16]))
				out = append(out, stcoLoc{tablePos: p + 16, count: count})
			case "moov", "trak", "mdia", "minf", "stbl":
				if err := walk(p+8, p+size); err != nil {
					return err
				}
			}
			p += size
		}
		return nil
	}
	if err := walk(0, int64(len(raw))); err != nil {
		return nil, err
	}
	return out, nil
}

func writeTrak(w *mp4.Writer, t WriteTrack) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if err := box(w, mp4.BoxTypeTkhd(), &mp4.Tkhd{TrackID: t.TrackID, DurationV0: 1,
		Volume: int16(t.Volume), Width: uint32(t.Width) << 16,
		Height: uint32(t.Height) << 16, Matrix: identityMatrix()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if err := box(w, mp4.BoxTypeMdhd(), &mp4.Mdhd{Timescale: t.Timescale,
		DurationV0: sumDeltas(t.Deltas)}); err != nil {
		return err
	}
	if err := writeHdlr(w, t.IsVideo); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if err := writeMediaHeader(w, t.IsVideo); err != nil {
		return err
	}
	if err := writeDinf(w); err != nil {
		return err
	}
	if err := writeStbl(w, t); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // minf
		return err
	}
	if _, err := w.EndBox(); err != nil { // mdia
		return err
	}
	_, err := w.EndBox() // trak
	return err
}

func writeHdlr(w *mp4.Writer, video bool) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeHdlr()}); err != nil {
		return err
	}
	h := &mp4.Hdlr{}
	if video {
		h.HandlerType = [4]byte{'v', 'i', 'd', 'e'}
	} else {
		h.HandlerType = [4]byte{'s', 'o', 'u', 'n'}
	}
	if err := marshal(w, h); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeMediaHeader(w *mp4.Writer, video bool) error {
	bt := mp4.BoxTypeVmhd()
	if !video {
		bt = mp4.BoxTypeSmhd()
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: bt}); err != nil {
		return err
	}
	if video {
		if err := marshal(w, &mp4.Vmhd{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 1}}}); err != nil {
			return err
		}
	} else if err := marshal(w, &mp4.Smhd{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeDinf(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDref()}); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Dref{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 0}},
		EntryCount: 1}); err != nil {
		return err
	}
	// The url entry is a nested child box of dref.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeUrl()}); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Url{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 1}}}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // url
		return err
	}
	if _, err := w.EndBox(); err != nil { // dref
		return err
	}
	_, err := w.EndBox() // dinf
	return err
}

func writeStbl(w *mp4.Writer, t WriteTrack) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if err := writeStsd(w, t); err != nil {
		return err
	}
	stts := &mp4.Stts{Entries: deltaRuns(t.Deltas)}
	stts.EntryCount = uint32(len(stts.Entries))
	if err := box(w, mp4.BoxTypeStts(), stts); err != nil {
		return err
	}
	if t.IsVideo {
		nums := make([]uint32, 0)
		for i, k := range t.Keyframes {
			if k {
				nums = append(nums, uint32(i+1))
			}
		}
		if err := box(w, mp4.BoxTypeStss(),
			&mp4.Stss{EntryCount: uint32(len(nums)), SampleNumber: nums}); err != nil {
			return err
		}
	}
	if err := box(w, mp4.BoxTypeStsz(),
		&mp4.Stsz{SampleCount: uint32(len(t.Sizes)), EntrySize: t.Sizes}); err != nil {
		return err
	}
	if err := box(w, mp4.BoxTypeStsc(), &mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{
		{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1},
	}}); err != nil {
		return err
	}
	offsets := make([]uint32, len(t.Sizes))
	if err := box(w, mp4.BoxTypeStco(),
		&mp4.Stco{EntryCount: uint32(len(offsets)), ChunkOffset: offsets}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeStsd(w *mp4.Writer, t WriteTrack) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Stsd{EntryCount: 1}); err != nil {
		return err
	}
	// The single sample entry is a nested child box (avc1 / mp4a).
	if t.IsVideo {
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeAvc1()}); err != nil {
			return err
		}
		if err := marshal(w, &mp4.VisualSampleEntry{
			SampleEntry: mp4.SampleEntry{
				AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeAvc1()},
				DataReferenceIndex: 1,
			},
			Width:  t.Width,
			Height: t.Height,
		}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
	} else {
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMp4a()}); err != nil {
			return err
		}
		if err := marshal(w, &mp4.AudioSampleEntry{
			SampleEntry: mp4.SampleEntry{
				AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeMp4a()},
				DataReferenceIndex: 1,
			},
		}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
	}
	_, err := w.EndBox() // stsd
	return err
}

func box(w *mp4.Writer, bt mp4.BoxType, payload mp4.IImmutableBox) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: bt}); err != nil {
		return err
	}
	if err := marshal(w, payload); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func marshal(w *mp4.Writer, b mp4.IImmutableBox) error {
	_, err := mp4.Marshal(w, b, mp4.Context{})
	return err
}

func sumDeltas(d []uint32) uint32 {
	var s uint64
	for _, v := range d {
		s += uint64(v)
	}
	return uint32(s)
}

func deltaRuns(d []uint32) []mp4.SttsEntry {
	if len(d) == 0 {
		return nil
	}
	out := make([]mp4.SttsEntry, 0)
	run := mp4.SttsEntry{SampleCount: 1, SampleDelta: d[0]}
	for i := 1; i < len(d); i++ {
		if d[i] == run.SampleDelta {
			run.SampleCount++
			continue
		}
		out = append(out, run)
		run = mp4.SttsEntry{SampleCount: 1, SampleDelta: d[i]}
	}
	return append(out, run)
}

func identityMatrix() [9]int32 {
	return [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
}
