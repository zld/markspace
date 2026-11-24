package demux

import (
	"mediapipeline/internal/clock"
	"mediapipeline/internal/fault"
)

// appendFragmentSamples resolves moof/trun sample tables into the demuxed
// tracks.  The output samples carry absolute input offsets and the track's own
// timescale, so later remux stages can move them without touching payload
// bytes.
func appendFragmentSamples(media *Media, top []*rawBox, data []byte, opts Options) error {
	dataLen := int64(len(data))
	trex := parseTrex(findTop(top, "moov"))
	byID := make(map[uint32]*Track, len(media.Tracks))
	for i := range media.Tracks {
		byID[media.Tracks[i].ID] = &media.Tracks[i]
	}
	seen := make(map[uint32]bool)
	for i, b := range top {
		if b.typ != "moof" {
			continue
		}
		var mdat *rawBox
		for j := i + 1; j < len(top); j++ {
			if top[j].typ == "mdat" {
				mdat = top[j]
				break
			}
			if top[j].typ == "moof" || top[j].typ == "moov" {
				break
			}
		}
		if mdat == nil {
			reason := "moof without following mdat"
			if opts.TolerateTruncated {
				media.Cut = append(media.Cut, Range{Start: b.off + b.size, End: dataLen})
				continue
			}
			return fault.New(fault.StageDemux, "mp4", fault.OpTruncated, b.off+b.size, reason)
		}
		layout, err := parseFragment(b, mdat.payloadOff(), dataLen, trex)
		if err != nil {
			return err
		}
		for id, samples := range layout.samples {
			tr, ok := byID[id]
			if !ok {
				if opts.TolerateTruncated {
					continue
				}
				return fault.New(fault.StageDemux, "mp4", fault.OpDemuxInvalid, b.off,
					"fragment references unknown track")
			}
			seen[id] = true
			for si := range samples {
				s := samples[si]
				start, end := s.Offset, s.Offset+int64(s.Size)
				if start < 0 || end > dataLen || start > end {
					return fault.New(fault.StageDemux, "mp4", fault.OpTruncated, dataLen,
						"fragment sample extends beyond input")
				}
				s.DTS = clock.New(s.DTS.Ticks, tr.Timescale)
				s.CTS = clock.New(s.CTS.Ticks, tr.Timescale)
				s.Data = data[start:end]
				tr.Samples = append(tr.Samples, s)
			}
		}
	}
	return nil
}
