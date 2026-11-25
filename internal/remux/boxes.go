package remux

import (
	"github.com/abema/go-mp4"

	"mediacore/internal/clock"
	"mediacore/internal/scan"
)

type trackConfig struct {
	track         scan.Track
	mapping       scan.TrackMapping
	items         []plannedSample
	startOffsetNS int64
	durationUnits uint64
	durationNS    int64
}

func writeFtyp(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	ftyp := &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 0x00000200,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
		},
	}
	if _, err := mp4.Marshal(w, ftyp, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func buildTrackConfigs(idx *scan.Index, mappings []scan.TrackMapping, plan []plannedSample) []trackConfig {
	byMap := map[uint32]*trackConfig{}
	var order []uint32
	for _, m := range mappings {
		tc := &trackConfig{mapping: m}
		for _, tr := range idx.Tracks {
			if tr.ID == m.SourceTrackID {
				tc.track = tr
			}
		}
		byMap[m.OutputTrackID] = tc
		order = append(order, m.OutputTrackID)
	}
	for _, ps := range plan {
		tc := byMap[ps.mapping.OutputTrackID]
		if tc != nil {
			tc.items = append(tc.items, ps)
		}
	}
	var minFirst int64 = -1
	for _, id := range order {
		tc := byMap[id]
		if len(tc.items) == 0 {
			continue
		}
		first := tc.items[0].ref.DTSNS
		if minFirst < 0 || first < minFirst {
			minFirst = first
		}
	}
	out := make([]trackConfig, 0, len(order))
	for _, id := range order {
		tc := byMap[id]
		if tc == nil || len(tc.items) == 0 {
			continue
		}
		tc.startOffsetNS = tc.items[0].ref.DTSNS - minFirst
		var durUnits int64
		for i, ps := range tc.items {
			cur := ps.ref.DTSUnits
			next := cur + int64(ps.ref.DurationUnits)
			if i+1 < len(tc.items) {
				next = tc.items[i+1].ref.DTSUnits
			}
			delta := next - cur
			if delta <= 0 {
				delta = int64(ps.ref.DurationUnits)
			}
			if delta <= 0 {
				delta = 1
			}
			durUnits += delta
		}
		tc.durationUnits = uint64(durUnits)
		tc.durationNS = clock.Timebase{Timescale: tc.track.Timescale}.ToNS(durUnits)
		out = append(out, *tc)
	}
	return out
}

func writeMoov(f interface {
	ioWriteSeeker
}, cfgs []trackConfig, idx *scan.Index) error {
	w := mp4.NewWriter(f)
	movieNS := int64(0)
	for _, tc := range cfgs {
		if d := tc.startOffsetNS + tc.durationNS; d > movieNS {
			movieNS = d
		}
	}
	movieMS := uint32(clock.DurationMS(movieNS) + 0.5)

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	if err := marshalBox(w, &mp4.Mvhd{
		FullBox: mp4.FullBox{Version: 0}, Timescale: 1000, DurationV0: movieMS,
		Rate: 0x00010000, Volume: 0x0100,
		Matrix:      [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000},
		NextTrackID: uint32(len(cfgs) + 1),
	}); err != nil {
		return err
	}
	for i, tc := range cfgs {
		if err := writeTrak(w, tc, uint32(i+1), movieMS); err != nil {
			return err
		}
	}
	_, err := w.EndBox()
	return err
}

type ioWriteSeeker interface {
	Write(p []byte) (int, error)
	Seek(offset int64, whence int) (int64, error)
}

func marshalBox(w *mp4.Writer, box mp4.IImmutableBox) error {
	_, err := mp4.Marshal(w, box, mp4.Context{})
	return err
}

func writeTrak(w *mp4.Writer, tc trackConfig, trackID uint32, movieMS uint32) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	volume := int16(0)
	width, height := uint32(0), uint32(0)
	if tc.track.Kind == "audio" {
		volume = 0x0100
	} else {
		width = uint32(tc.track.Width) << 16
		height = uint32(tc.track.Height) << 16
	}
	if err := marshalBox(w, &mp4.Tkhd{
		FullBox:    mp4.FullBox{Flags: [3]byte{0, 0, 0x07}},
		TrackID:    trackID,
		DurationV0: movieMS,
		Volume:     volume,
		Width:      width,
		Height:     height,
		Matrix:     [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000},
	}); err != nil {
		return err
	}
	if err := writeEdts(w, tc, movieMS); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if err := marshalBox(w, &mp4.Mdhd{
		Timescale: tc.track.Timescale, DurationV0: uint32(tc.durationUnits),
	}); err != nil {
		return err
	}
	handler := [4]byte{'v', 'i', 'd', 'e'}
	if tc.track.Kind == "audio" {
		handler = [4]byte{'s', 'o', 'u', 'n'}
	}
	if err := marshalBox(w, &mp4.Hdlr{HandlerType: handler, Name: "mediacore-" + tc.track.Kind}); err != nil {
		return err
	}
	if err := writeMinf(w, tc); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // mdia
		return err
	}
	_, err := w.EndBox() // trak
	return err
}

func writeEdts(w *mp4.Writer, tc trackConfig, movieMS uint32) error {
	entries := []mp4.ElstEntry{}
	if len(tc.track.Edits) > 0 {
		mb := clock.Timebase{Timescale: 1000}
		for _, e := range tc.track.Edits {
			entries = append(entries, mp4.ElstEntry{
				SegmentDurationV0: uint32(mb.FromNS(e.SegmentDurationNS)),
				MediaTimeV0:       int32(clock.Timebase{Timescale: tc.track.Timescale}.FromNS(e.MediaTimeNS)),
				MediaRateInteger:  e.MediaRate,
			})
		}
	} else if tc.startOffsetNS > 500_000 {
		entries = []mp4.ElstEntry{
			{SegmentDurationV0: uint32(clock.DurationMS(tc.startOffsetNS) + 0.5), MediaTimeV0: -1, MediaRateInteger: 1},
			{SegmentDurationV0: movieMS, MediaTimeV0: 0, MediaRateInteger: 1},
		}
	}
	if len(entries) == 0 {
		return nil
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeEdts()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeElst()}); err != nil {
		return err
	}
	if err := marshalBox(w, &mp4.Elst{EntryCount: uint32(len(entries)), Entries: entries}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // elst
		return err
	}
	_, err := w.EndBox() // edts
	return err
}

func writeMinf(w *mp4.Writer, tc trackConfig) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if tc.track.Kind == "audio" {
		if err := marshalBox(w, &mp4.Smhd{}); err != nil {
			return err
		}
	} else {
		if err := marshalBox(w, &mp4.Vmhd{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 1}}}); err != nil {
			return err
		}
	}
	if err := writeDinf(w); err != nil {
		return err
	}
	if err := writeStbl(w, tc); err != nil {
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
	if err := marshalBox(w, &mp4.Dref{EntryCount: 1}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeUrl()}); err != nil {
		return err
	}
	if err := marshalBox(w, &mp4.Url{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 1}}}); err != nil {
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
