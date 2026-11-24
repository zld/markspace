package media

import (
	"fmt"

	mp4 "github.com/abema/go-mp4"
)

// BuildMP4 builds a deterministic MP4 with mdat before moov so sample offsets
// are known without patching stco.
func BuildMP4(tracks []Track) ([]byte, error) {
	if len(tracks) == 0 {
		return nil, fmt.Errorf("media: at least one track is required")
	}
	for i := range tracks {
		if tracks[i].Timescale == 0 {
			return nil, fmt.Errorf("media: track %d timescale required", i)
		}
		tracks[i].offsets = make([]uint32, 0, len(tracks[i].Samples))
	}
	var buf WriteSeekerBuffer
	w := mp4.NewWriter(&buf)

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand:       [4]byte{'i', 's', 'o', 'm'},
		MinorVersion:     0x200,
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}}},
	}, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return nil, err
	}
	for ti := range tracks {
		for _, sample := range tracks[ti].Samples {
			pos, err := w.Seek(0, seekCurrent)
			if err != nil {
				return nil, err
			}
			if pos > int64(^uint32(0)) {
				return nil, fmt.Errorf("media: mdat offset exceeds uint32 stco")
			}
			tracks[ti].offsets = append(tracks[ti].offsets, uint32(pos))
			if _, err := w.Write(sample.Data); err != nil {
				return nil, err
			}
		}
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}

	var movieDuration uint32
	for _, t := range tracks {
		d := trackDuration(t)
		if d > movieDuration {
			movieDuration = d
		}
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, &mp4.Mvhd{
		Timescale:   90000,
		DurationV0:  movieDuration,
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      identityMatrix(),
		NextTrackID: uint32(len(tracks) + 1),
	}, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	for _, t := range tracks {
		if err := writeTrak(w, t); err != nil {
			return nil, fmt.Errorf("trak %d: %w", t.ID, err)
		}
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

const seekCurrent = 1 // io.SeekCurrent without importing io in this compact file.

func trackDuration(t Track) uint32 {
	var d uint64
	for _, s := range t.Samples {
		delta := s.Delta
		if delta == 0 {
			delta = 1
		}
		d += uint64(delta)
	}
	return uint32(d)
}

func identityMatrix() [9]int32 {
	return [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}
}

func writeTrak(w *mp4.Writer, t Track) error {
	handler := [4]byte{'v', 'i', 'd', 'e'}
	if t.Kind == "audio" || t.Kind == "soun" {
		handler = [4]byte{'s', 'o', 'u', 'n'}
	}
	duration := trackDuration(t)
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTkhd()}); err != nil {
		return err
	}
	tkhd := &mp4.Tkhd{TrackID: t.ID, DurationV0: duration, Matrix: identityMatrix()}
	if handler[0] == 's' {
		tkhd.Volume = 0x0100
	} else {
		tkhd.Width = uint32(t.Width) << 16
		tkhd.Height = uint32(t.Height) << 16
	}
	if _, err := mp4.Marshal(w, tkhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Mdhd{Timescale: t.Timescale, DurationV0: duration, Language: [3]byte{'u', 'n', 'd'}}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeHdlr()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Hdlr{HandlerType: handler, Name: "mediarc deterministic track"}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if err := writeMediaHeader(w, handler); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if err := writeSampleTables(w, t); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // stbl
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
	return nil
}

func writeMediaHeader(w *mp4.Writer, handler [4]byte) error {
	if handler[0] == 's' {
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeSmhd()}); err != nil {
			return err
		}
		if _, err := mp4.Marshal(w, &mp4.Smhd{}, mp4.Context{}); err != nil {
			return err
		}
		_, err := w.EndBox()
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeVmhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Vmhd{}, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}
