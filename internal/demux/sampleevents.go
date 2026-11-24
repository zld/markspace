package demux

import (
	"encoding/binary"

	"mediapipeline/internal/fault"
	"mediapipeline/internal/metadata"
	"mediapipeline/internal/track"
)

// annexBSEIEvents finds user_data_unregistered SEI messages in Annex B video
// samples.  Source offsets point at the sample start because an Annex B sample
// assembled from transport packets has no single contiguous source position.
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

// id3Size returns the total size of an ID3v2 tag including its 10-byte header
// and (when present) footer.
func id3Size(p []byte) int {
	if len(p) < 10 || string(p[:3]) != "ID3" {
		return 0
	}
	size := int(p[6]&0x7f)<<21 | int(p[7]&0x7f)<<14 | int(p[8]&0x7f)<<7 | int(p[9]&0x7f)
	total := 10 + size
	if p[5]&0x10 != 0 {
		total += 10
	}
	if total > len(p) {
		return len(p)
	}
	return total
}

// seiEvents returns one event per user_data_unregistered SEI message found in
// a video track's samples.  Samples are length-prefixed with the track's NAL
// length size.
func seiEvents(tr *Track) ([]metadata.Event, error) {
	if tr.NALLengthSize == 0 {
		return annexBSEIEvents(tr), nil
	}
	n := int(tr.NALLengthSize)
	var out []metadata.Event
	for si := range tr.Samples {
		s := &tr.Samples[si]
		pos := 0
		for pos+n <= len(s.Data) {
			size := 0
			for k := 0; k < n; k++ {
				size = size<<8 | int(s.Data[pos+k])
			}
			pos += n
			if size <= 0 || pos+size > len(s.Data) {
				return nil, fault.New(fault.StageDemux, "mp4", fault.OpTruncated, s.Offset+int64(pos-n),
					"NAL length exceeds sample payload")
			}
			nal := s.Data[pos : pos+size]
			if len(nal) > 0 && nal[0]&0x1f == 6 {
				msgs := seiMessages(nal[1:])
				for mi, m := range msgs {
					if m.typ != 5 {
						continue
					}
					out = append(out, metadata.Event{
						Kind: metadata.KindSEI, TrackID: tr.ID, Time: s.CTS,
						Data: m.payload, SourceOffset: s.Offset + int64(pos) + int64(m.offset),
					})
					_ = mi
				}
			}
			pos += size
		}
	}
	return out, nil
}

type seiMessage struct {
	typ     int
	payload []byte
	offset  int
}

// seiMessages splits a SEI RBSP (without the NAL header) into messages.
func seiMessages(rbsp []byte) []seiMessage {
	var out []seiMessage
	pos := 0
	for pos < len(rbsp) {
		if rbsp[pos] == 0x80 && pos == len(rbsp)-1 {
			break // rbsp_trailing_bits
		}
		typ := 0
		for pos < len(rbsp) && rbsp[pos] == 0xff {
			typ += 255
			pos++
		}
		if pos >= len(rbsp) {
			break
		}
		typ += int(rbsp[pos])
		pos++
		size := 0
		for pos < len(rbsp) && rbsp[pos] == 0xff {
			size += 255
			pos++
		}
		if pos >= len(rbsp) {
			break
		}
		size += int(rbsp[pos])
		pos++
		if size < 0 || pos+size > len(rbsp) {
			break
		}
		out = append(out, seiMessage{typ: typ, payload: rbsp[pos : pos+size], offset: pos})
		pos += size
	}
	return out
}
