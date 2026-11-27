// Package media builds small, structurally valid MP4 assets in memory so
// the replay scenarios can exercise real go-mp4 parsing without shipping
// binary fixtures. Every file contains one media track with one sample
// whose payload is the caller-supplied essence.
package media

import (
	"fmt"
	"io"

	"github.com/abema/go-mp4"
)

// Asset describes one synthetic MP4 to build.
type Asset struct {
	// TrackID is the MP4 track ID.
	TrackID uint32
	// Timescale is the movie and media timescale in hertz.
	Timescale uint32
	// DurationTicks is the media duration in timescale ticks.
	DurationTicks uint32
	// Essence is the sample payload. Its bytes determine the file
	// digest, so differing essence means differing content.
	Essence []byte
	// Handler is the media handler type ("soun" or "vide").
	Handler [4]byte
	// CreationTime, when non-zero, stamps the mp4 header so two builds
	// with the same essence are byte-identical.
	CreationTime uint32
}

// memFile is a growable in-memory io.WriteSeeker.
type memFile struct {
	buf []byte
	pos int64
}

func (m *memFile) Write(p []byte) (int, error) {
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

func (m *memFile) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
		base = 0
	case io.SeekCurrent:
		base = m.pos
	case io.SeekEnd:
		base = int64(len(m.buf))
	default:
		return 0, fmt.Errorf("invalid whence %d", whence)
	}
	next := base + offset
	if next < 0 {
		return 0, fmt.Errorf("negative seek to %d", next)
	}
	m.pos = next
	return next, nil
}

// Build serialises a minimal fast-start-free MP4 (ftyp, mdat, moov) and
// returns its bytes. Output is deterministic for a given Asset.
func Build(a Asset) ([]byte, error) {
	if a.Timescale == 0 {
		return nil, fmt.Errorf("media: timescale must be positive")
	}
	if a.TrackID == 0 {
		return nil, fmt.Errorf("media: track ID must be positive")
	}
	if len(a.Essence) == 0 {
		return nil, fmt.Errorf("media: essence must not be empty")
	}
	if a.Handler == [4]byte{} {
		a.Handler = [4]byte{'s', 'o', 'u', 'n'}
	}

	mf := &memFile{}
	w := mp4.NewWriter(mf)

	if err := writeBox(w, mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}, &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 512,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
		},
	}); err != nil {
		return nil, err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return nil, err
	}
	dataOffset := mf.pos
	if _, err := w.Write(a.Essence); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}

	if err := writeMoov(w, a, dataOffset); err != nil {
		return nil, err
	}

	return append([]byte(nil), mf.buf...), nil
}

func writeMoov(w *mp4.Writer, a Asset, dataOffset int64) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	matrix := [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
	if err := writeBox(w, mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}, &mp4.Mvhd{
		FullBox:        mp4.FullBox{Version: 0},
		CreationTimeV0: a.CreationTime,
		Timescale:      a.Timescale,
		DurationV0:     a.DurationTicks,
		Rate:           0x00010000,
		Volume:         0x0100,
		Matrix:         matrix,
		NextTrackID:    a.TrackID + 1,
	}); err != nil {
		return err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if err := writeBox(w, mp4.BoxInfo{Type: mp4.BoxTypeTkhd()}, &mp4.Tkhd{
		FullBox:        mp4.FullBox{Version: 0, Flags: [3]byte{0, 0, 0x07}},
		CreationTimeV0: a.CreationTime,
		TrackID:        a.TrackID,
		DurationV0:     a.DurationTicks,
		Volume:         0x0100,
		Matrix:         matrix,
	}); err != nil {
		return err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if err := writeBox(w, mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}, &mp4.Mdhd{
		FullBox:        mp4.FullBox{Version: 0},
		CreationTimeV0: a.CreationTime,
		Timescale:      a.Timescale,
		DurationV0:     a.DurationTicks,
		Language:       [3]byte{'u', 'n', 'd'},
	}); err != nil {
		return err
	}
	if err := writeBox(w, mp4.BoxInfo{Type: mp4.BoxTypeHdlr()}, &mp4.Hdlr{
		FullBox:     mp4.FullBox{Version: 0},
		HandlerType: a.Handler,
		Name:        "replay media handler",
	}); err != nil {
		return err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if err := writeMediaHeader(w, a.Handler); err != nil {
		return err
	}
	if err := writeDinf(w); err != nil {
		return err
	}
	if err := writeStbl(w, a, dataOffset); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // minf
		return err
	}
	if _, err := w.EndBox(); err != nil { // mdia
		return err
	}
	if _, err := w.EndBox(); err != nil { // trak
		return err
	}
	if _, err := w.EndBox(); err != nil { // moov
		return err
	}
	return nil
}

func writeMediaHeader(w *mp4.Writer, handler [4]byte) error {
	var header mp4.IImmutableBox
	if handler == [4]byte{'v', 'i', 'd', 'e'} {
		header = &mp4.Vmhd{FullBox: mp4.FullBox{Version: 0, Flags: [3]byte{0, 0, 1}}}
	} else {
		header = &mp4.Smhd{FullBox: mp4.FullBox{Version: 0}}
	}
	return writeBox(w, mp4.BoxInfo{Type: header.GetType()}, header)
}

func writeDinf(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDref()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Dref{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	if err := writeBox(w, mp4.BoxInfo{Type: mp4.BoxTypeUrl()}, &mp4.Url{
		FullBox: mp4.FullBox{Version: 0, Flags: [3]byte{0, 0, 1}},
	}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	return endBox(w)
}

func writeStbl(w *mp4.Writer, a Asset, dataOffset int64) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	// A zero-entry stsd keeps the structure minimal; probes only need
	// stts/stsc/stsz/stco for sample accounting.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 0}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if err := writeBox(w, mp4.BoxInfo{Type: mp4.BoxTypeStts()}, &mp4.Stts{
		EntryCount: 1,
		Entries:    []mp4.SttsEntry{{SampleCount: 1, SampleDelta: a.DurationTicks}},
	}); err != nil {
		return err
	}
	if err := writeBox(w, mp4.BoxInfo{Type: mp4.BoxTypeStsc()}, &mp4.Stsc{
		EntryCount: 1,
		Entries:    []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1}},
	}); err != nil {
		return err
	}
	if err := writeBox(w, mp4.BoxInfo{Type: mp4.BoxTypeStsz()}, &mp4.Stsz{
		SampleSize:  0,
		SampleCount: 1,
		EntrySize:   []uint32{uint32(len(a.Essence))},
	}); err != nil {
		return err
	}
	if err := writeBox(w, mp4.BoxInfo{Type: mp4.BoxTypeStco()}, &mp4.Stco{
		EntryCount:  1,
		ChunkOffset: []uint32{uint32(dataOffset)},
	}); err != nil {
		return err
	}
	return endBox(w)
}

func writeBox(w *mp4.Writer, info mp4.BoxInfo, box mp4.IImmutableBox) error {
	if _, err := w.StartBox(&info); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, box, mp4.Context{}); err != nil {
		return err
	}
	return endBox(w)
}

func endBox(w *mp4.Writer) error {
	_, err := w.EndBox()
	return err
}

// EssenceFor synthesises deterministic essence bytes for a version and
// track, with an optional salt that changes content without changing
// length.
func EssenceFor(track, version string, salt byte, size int) []byte {
	out := make([]byte, size)
	seed := uint32(2166136261)
	for _, ch := range []byte(track + "/" + version) {
		seed ^= uint32(ch)
		seed *= 16777619
	}
	for i := range out {
		seed = seed*1664525 + 1013904223
		out[i] = byte(seed >> 24)
	}
	if len(out) > 0 {
		out[0] = salt
	}
	return out
}
