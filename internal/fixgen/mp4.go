package fixgen

import (
	"io"

	"github.com/abema/go-mp4"
)

// TrackSpec describes one synthesized MP4 track.
type TrackSpec struct {
	ID        uint32
	Kind      string // "audio" or "video"
	Timescale uint32
	Deltas    []uint32 // per-sample durations on the track timescale; 0 = anomalous
	Sizes     []uint32 // per-sample byte sizes; 0 marks a corrupt sample
	Keyframes []uint32 // 1-based key sample numbers; empty = every sample is key
}

var identityMatrix = [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}

func leaf(w *mp4.Writer, typ mp4.BoxType, b mp4.IImmutableBox) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: typ}); err != nil {
		return err
	}
	if b != nil {
		if _, err := mp4.Marshal(w, b, mp4.Context{}); err != nil {
			return err
		}
	}
	_, err := w.EndBox()
	return err
}

func stts(deltas []uint32) *mp4.Stts {
	s := &mp4.Stts{}
	for _, d := range deltas {
		if n := len(s.Entries); n > 0 && s.Entries[n-1].SampleDelta == d {
			s.Entries[n-1].SampleCount++
		} else {
			s.Entries = append(s.Entries, mp4.SttsEntry{SampleCount: 1, SampleDelta: d})
		}
	}
	s.EntryCount = uint32(len(s.Entries))
	return s
}

// WriteMP4 writes a minimal but well-formed MP4 with the given tracks.
func WriteMP4(out io.WriteSeeker, tracks []TrackSpec) error {
	w := mp4.NewWriter(out)
	if err := leaf(w, mp4.BoxTypeFtyp(), &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 1,
	}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	var movieDur uint32
	durs := make([]uint32, len(tracks))
	for i, t := range tracks {
		for _, d := range t.Deltas {
			durs[i] += d
		}
		if ms := durs[i] * 1000 / t.Timescale; ms > movieDur {
			movieDur = ms
		}
	}
	if err := leaf(w, mp4.BoxTypeMvhd(), &mp4.Mvhd{
		Timescale: 1000, DurationV0: movieDur,
		Rate: 0x00010000, Volume: 0x0100, Matrix: identityMatrix,
		NextTrackID: uint32(len(tracks)) + 1,
	}); err != nil {
		return err
	}
	for i, t := range tracks {
		if err := writeTrack(w, t, durs[i]); err != nil {
			return err
		}
	}
	if _, err := w.EndBox(); err != nil { // moov
		return err
	}
	// mdat with zero-filled sample payload bytes.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return err
	}
	var total int64
	for _, t := range tracks {
		for _, s := range t.Sizes {
			total += int64(s)
		}
	}
	if _, err := w.Write(make([]byte, total)); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeTrack(w *mp4.Writer, t TrackSpec, dur uint32) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	tk := &mp4.Tkhd{
		FullBox:    mp4.FullBox{Flags: [3]byte{0, 0, 3}},
		TrackID:    t.ID,
		DurationV0: dur * 1000 / t.Timescale,
		Matrix:     identityMatrix,
	}
	if t.Kind == "audio" {
		tk.Volume = 0x0100
	} else {
		tk.Width, tk.Height = 640<<16, 360<<16
	}
	if err := leaf(w, mp4.BoxTypeTkhd(), tk); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if err := leaf(w, mp4.BoxTypeMdhd(), &mp4.Mdhd{
		Timescale: t.Timescale, DurationV0: dur, Language: [3]byte{'u', 'n', 'd'},
	}); err != nil {
		return err
	}
	hdlr := [4]byte{'v', 'i', 'd', 'e'}
	if t.Kind == "audio" {
		hdlr = [4]byte{'s', 'o', 'u', 'n'}
	}
	if err := leaf(w, mp4.BoxTypeHdlr(), &mp4.Hdlr{HandlerType: hdlr, Name: "fixgen"}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if t.Kind == "audio" {
		if err := leaf(w, mp4.BoxTypeSmhd(), &mp4.Smhd{}); err != nil {
			return err
		}
	} else if err := leaf(w, mp4.BoxTypeVmhd(), &mp4.Vmhd{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 1}}}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDref()}); err != nil {
		return err
	}
	if err := leaf(w, mp4.BoxTypeUrl(), &mp4.Url{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 1}}}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // dref
		return err
	}
	if _, err := w.EndBox(); err != nil { // dinf
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if err := leaf(w, mp4.BoxTypeStsd(), &mp4.Stsd{}); err != nil {
		return err
	}
	if err := leaf(w, mp4.BoxTypeStts(), stts(t.Deltas)); err != nil {
		return err
	}
	if len(t.Keyframes) > 0 && len(t.Keyframes) < len(t.Deltas) {
		if err := leaf(w, mp4.BoxTypeStss(), &mp4.Stss{
			EntryCount: uint32(len(t.Keyframes)), SampleNumber: t.Keyframes,
		}); err != nil {
			return err
		}
	}
	if err := leaf(w, mp4.BoxTypeStsc(), &mp4.Stsc{}); err != nil {
		return err
	}
	if err := leaf(w, mp4.BoxTypeStsz(), &mp4.Stsz{
		SampleCount: uint32(len(t.Sizes)), EntrySize: t.Sizes,
	}); err != nil {
		return err
	}
	if err := leaf(w, mp4.BoxTypeStco(), &mp4.Stco{}); err != nil {
		return err
	}
	for _, typ := range []mp4.BoxType{
		mp4.BoxTypeStbl(), mp4.BoxTypeMinf(), mp4.BoxTypeMdia(), mp4.BoxTypeTrak(),
	} {
		_ = typ
		if _, err := w.EndBox(); err != nil {
			return err
		}
	}
	return nil
}
