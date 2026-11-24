package demux

import (
	"mediapipeline/internal/metadata"
)

func annexBSEIEvents(tr *Track) []metadata.Event {
	var out []metadata.Event
	for si := range tr.Samples {
		s := &tr.Samples[si]
		for _, nal := range annexBNALUs(s.Data) {
			if len(nal.data) == 0 || nal.data[0]&0x1f != 6 {
				continue
			}
			for _, m := range seiMessages(nal.data[1:]) {
				if m.typ != 5 {
					continue
				}
				out = append(out, metadata.Event{
					Kind: metadata.KindSEI, TrackID: tr.ID, Time: s.CTS, Data: m.payload,
					SourceOffset: s.Offset,
				})
			}
		}
	}
	return out
}

// deriveSampleEvents lifts timed metadata out of media samples: subtitles and
// chapter text from subtitle tracks, ID3 tags from metadata tracks and
// user_data_unregistered SEI messages from video samples.
