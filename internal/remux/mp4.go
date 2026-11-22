package remux

import (
	"fmt"
	"io"

	mp4 "github.com/abema/go-mp4"

	"mediasync/internal/demux"
)

// WriteMP4Sidecar emits a minimal, standards-shaped MP4 package for one
// mapped track using abema/go-mp4 for the box structure. Codec payloads are
// copied verbatim into mdat; the file exists to prove the same lossless
// samples can be carried by a second container, not to re-encode anything.
func WriteMP4Sidecar(w io.WriteSeeker, track *demux.Track, samples []mergedSample) error {
	if track == nil {
		return fmt.Errorf("remux: nil track for mp4 sidecar")
	}
	mw := mp4.NewWriter(w)

	// ftyp
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(mw, &mp4.Ftyp{
		MajorBrand: [4]byte{'i', 's', 'o', 'm'},
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '2'}},
		},
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}

	// moov > mvhd/trak(mdia(mdhd/hdlr/minf(stbl)))
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	if err := writeMvhd(mw, samples); err != nil {
		return err
	}
	if err := writeTrak(mw, track, samples); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil { // moov
		return err
	}

	// mdat with verbatim payloads concatenated in sample order.
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return err
	}
	for _, s := range samples {
		if s.Track != track.Number {
			continue
		}
		if _, err := mw.Write(s.Payload); err != nil {
			return err
		}
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	return nil
}

func writeMvhd(mw *mp4.Writer, samples []mergedSample) error {
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); err != nil {
		return err
	}
	duration := uint32(0)
	if len(samples) > 0 {
		duration = uint32(samples[len(samples)-1].NS / 1_000_000) // ms timescale 1000
	}
	if _, err := mp4.Marshal(mw, &mp4.Mvhd{
		Timescale:   1000,
		DurationV0:  duration,
		NextTrackID: 2,
	}, mp4.Context{}); err != nil {
		return err
	}
	_, err := mw.EndBox()
	return err
}

func writeTrak(mw *mp4.Writer, track *demux.Track, samples []mergedSample) error {
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	// tkhd
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTkhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(mw, &mp4.Tkhd{
		TrackID:    track.Number,
		DurationV0: uint32(trackDurationMS(samples)),
		Width:      uint32(track.VideoWidth) << 16,
		Height:     uint32(track.VideoHeight) << 16,
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	// mdia
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if err := writeMdhd(mw, track, samples); err != nil {
		return err
	}
	if err := writeHdlrMinf(mw, track, samples); err != nil {
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

func writeMdhd(mw *mp4.Writer, track *demux.Track, samples []mergedSample) error {
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return err
	}
	timescale := uint32(1_000_000_000 / uint32(track.TimecodeScaleNS))
	if timescale == 0 {
		timescale = 1000
	}
	if _, err := mp4.Marshal(mw, &mp4.Mdhd{
		Timescale:  timescale,
		DurationV0: uint32(trackDurationMS(samples)),
	}, mp4.Context{}); err != nil {
		return err
	}
	_, err := mw.EndBox()
	return err
}

func writeHdlrMinf(mw *mp4.Writer, track *demux.Track, samples []mergedSample) error {
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeHdlr()}); err != nil {
		return err
	}
	handler := "soun"
	if track.Type == demux.TypeVideo {
		handler = "vide"
	}
	name := handler + "\x00"
	if _, err := mp4.Marshal(mw, &mp4.Hdlr{
		HandlerType: strTo4(handler),
		Name:        name,
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if err := writeStbl(mw, track, samples); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil { // minf
		return err
	}
	return nil
}

func firstLast(samples []mergedSample, track *demux.Track) (int64, int64) {
	var first, last int64
	seen := false
	for _, s := range samples {
		if s.Track != track.Number {
			continue
		}
		if !seen {
			first, seen = s.NS, true
		}
		last = s.NS
	}
	return first, last
}

func trackDurationMS(samples []mergedSample) uint64 {
	if len(samples) == 0 {
		return 0
	}
	return uint64(samples[len(samples)-1].NS / 1_000_000)
}

func strTo4(s string) [4]byte {
	var out [4]byte
	copy(out[:], s)
	return out
}
