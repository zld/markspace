package demux

import (
	"encoding/binary"

	"mediapipeline/internal/metadata"
	"mediapipeline/internal/track"
)

func deriveSampleEvents(media *Media) ([]metadata.Event, error) {
	var out []metadata.Event
	for ti := range media.Tracks {
		tr := &media.Tracks[ti]
		switch {
		case tr.Kind == track.KindSubtitle || tr.Kind == track.KindChapter:
			kind := metadata.KindSubtitle
			if tr.Kind == track.KindChapter {
				kind = metadata.KindChapter
			}
			for si := range tr.Samples {
				s := &tr.Samples[si]
				if len(s.Data) == 0 {
					continue
				}
				data := s.Data
				if tr.Codec == "tx3g" && len(data) >= 2 {
					n := int(binary.BigEndian.Uint16(data))
					if 2+n <= len(data) {
						data = data[2 : 2+n]
					}
				}
				out = append(out, metadata.Event{
					Kind: kind, TrackID: tr.ID, Time: s.CTS,
					Data: data, SourceOffset: s.Offset,
				})
			}
		case tr.Kind == track.KindMetadata && (tr.Codec == "ID3 " || tr.Codec == "id3 "):
			for si := range tr.Samples {
				s := &tr.Samples[si]
				payload := s.Data
				if len(payload) < 10 || string(payload[:3]) != "ID3" {
					continue
				}
				size := id3Size(payload)
				if size > len(payload) {
					size = len(payload)
				}
				out = append(out, metadata.Event{
					Kind: metadata.KindID3, TrackID: tr.ID, Time: s.CTS,
					Data: payload[:size], SourceOffset: s.Offset,
				})
			}
		case tr.Kind == track.KindVideo:
			evs, err := seiEvents(tr)
			if err != nil {
				return nil, err
			}
			out = append(out, evs...)
		}
	}
	return out, nil
}
