package container

import (
	"fmt"
	"strings"

	"github.com/abema/go-mp4"

	"github.com/zld/mediarc/internal/model"
)

func (f *File) assembleTracks(cfg model.Config) error {
	trackCount := 0
	for _, b := range f.Boxes {
		if b.TrackIndex+1 > trackCount {
			trackCount = b.TrackIndex + 1
		}
	}
	f.Tracks = make([]*Track, 0, trackCount)
	for i := 0; i < trackCount; i++ {
		f.Tracks = append(f.Tracks, &Track{})
	}
	for _, b := range f.Boxes {
		if b.TrackIndex < 0 || b.TrackIndex >= len(f.Tracks) || b.Payload == nil {
			continue
		}
		tr := f.Tracks[b.TrackIndex]
		switch p := b.Payload.(type) {
		case *mp4.Tkhd:
			tr.Tkhd = p
		case *mp4.Mdhd:
			tr.Mdhd = p
		case *mp4.Hdlr:
			tr.Hdlr = p
		case *mp4.Stts:
			tr.Stts = p
		case *mp4.Stss:
			tr.Stss = p
		}
	}

	for i, tr := range f.Tracks {
		trackID := uint32(i + 1)
		if tr.Tkhd != nil && tr.Tkhd.TrackID != 0 {
			trackID = tr.Tkhd.TrackID
		}
		trakOffset := f.boxOffset(i, mp4.BoxTypeTrak().String())
		if tr.Tkhd == nil {
			return parseTrackError(trackID, "tkhd", trakOffset, "missing required tkhd box")
		}
		if tr.Mdhd == nil {
			return parseTrackError(trackID, "mdhd", trakOffset, "missing required mdhd box")
		}
		mdhdOffset := f.boxOffset(i, mp4.BoxTypeMdhd().String())
		if tr.Mdhd.Timescale == 0 {
			return &model.ParseError{TrackID: int(trackID), BoxType: "mdhd", ByteOffset: mdhdOffset, Err: model.ErrZeroTimescale}
		}

		label := labelFor(trackID, tr.Hdlr)
		events, err := tr.buildEvents(int(trackID), label, f.boxOffset(i, mp4.BoxTypeStts().String()), cfg)
		if err != nil {
			return err
		}
		if err := applyKeyframes(tr, events, int(trackID), f.boxOffset(i, mp4.BoxTypeStss().String())); err != nil {
			return err
		}
		tr.Events = events
		tr.Metadata = model.TrackMetadata{
			TrackID:        trackID,
			Representation: label,
			HandlerType:    handlerType(tr.Hdlr),
			Timescale:      tr.Mdhd.Timescale,
			MdhdOffset:     mdhdOffset,
			EventCount:     len(events),
			Empty:          len(events) == 0,
		}
	}
	return nil
}

func applyKeyframes(tr *Track, events []model.RawEvent, trackID int, stssOffset int64) error {
	if tr.Stss == nil {
		return nil
	}
	keySet := make(map[uint32]bool, len(tr.Stss.SampleNumber))
	for _, n := range tr.Stss.SampleNumber {
		if n == 0 || int(n) > len(events) {
			return &model.ParseError{
				TrackID:    trackID,
				BoxType:    "stss",
				ByteOffset: stssOffset,
				Err:        fmt.Errorf("keyframe sample number %d outside %d samples", n, len(events)),
			}
		}
		keySet[n] = true
	}
	for j := range events {
		events[j].Keyframe = keySet[events[j].SampleNumber]
	}
	return nil
}

func labelFor(trackID uint32, h *mp4.Hdlr) string {
	if h == nil {
		return fmt.Sprintf("track-%d", trackID)
	}
	if name := strings.TrimSpace(h.Name); name != "" {
		return name
	}
	if handler := handlerType(h); handler != "" {
		return fmt.Sprintf("%s-%d", handler, trackID)
	}
	return fmt.Sprintf("track-%d", trackID)
}

func (f *File) boxOffset(trackIndex int, typ string) int64 {
	for _, b := range f.Boxes {
		if b.TrackIndex == trackIndex && b.Type == typ {
			return b.Offset
		}
	}
	return -1
}

func handlerType(h *mp4.Hdlr) string {
	if h == nil {
		return ""
	}
	return strings.TrimSpace(string(h.HandlerType[:]))
}

func parseTrackError(id uint32, box string, offset int64, msg string) error {
	return &model.ParseError{TrackID: int(id), BoxType: box, ByteOffset: offset, Err: fmt.Errorf("%s", msg)}
}
