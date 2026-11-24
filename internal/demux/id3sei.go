package demux

import (
	"mediapipeline/internal/fault"
	"mediapipeline/internal/metadata"
)

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
