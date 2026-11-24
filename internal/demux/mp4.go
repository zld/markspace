package demux

import (
	"bytes"

	"github.com/abema/go-mp4"

	"mediapipeline/internal/fault"
)

// ParseMP4 demuxes a complete MP4/fMP4 byte slice.  All returned sample data
// aliases data.
func ParseMP4(data []byte, opts Options) (*Media, error) {
	top, cut, err := parseBoxes(data, 0, int64(len(data)), "mp4", opts.TolerateTruncated)
	if err != nil {
		return nil, err
	}
	media := &Media{Format: "mp4", Cut: cut}
	movieTimescale := uint32(1000)
	if mvhd := findTop(top, "moov"); mvhd != nil {
		if t := movieTimescaleOf(mvhd); t != 0 {
			movieTimescale = t
		}
	}
	if ftyp := findTop(top, "ftyp"); ftyp != nil {
		var f mp4.Ftyp
		payload := ftyp.payload()
		if _, err := mp4.Unmarshal(bytes.NewReader(payload), uint64(len(payload)), &f, mp4.Context{}); err == nil {
			media.MajorBrand = string(f.MajorBrand[:])
		}
	}
	moofs := findTops(top, "moof")
	fragmented := len(moofs) > 0
	if moov := findTop(top, "moov"); moov != nil {
		tracks, err := parseMoov(moov, fragmented, data, movieTimescale)
		if err != nil {
			return nil, err
		}
		media.Tracks = tracks
	}
	if len(media.Cut) > 0 {
		cutStart := media.Cut[0].Start
		if err := trimSamplesBefore(media.Tracks, cutStart); err != nil {
			return nil, err
		}
	}
	if fragmented && !opts.AllowMissingIndex && !hasIndex(top) {
		return nil, fault.New(fault.StageDemux, "mp4", fault.OpIndexIncomplete, moofs[0].off,
			"fragmented input has neither sidx nor mfra/tfra index")
	}
	if fragmented {
		if err := appendFragmentSamples(media, top, data, opts); err != nil {
			return nil, err
		}
	}
	events, err := collectEmsgEvents(top)
	if err != nil {
		return nil, err
	}
	if opts.SampleEvents {
		sampleEvents, err := deriveSampleEvents(media)
		if err != nil {
			return nil, err
		}
		events = append(events, sampleEvents...)
	}
	if len(media.Cut) > 0 {
		// Keep events whose source byte range is still present.
		kept := events[:0]
		for _, e := range events {
			if e.SourceOffset < media.Cut[0].Start {
				kept = append(kept, e)
			}
		}
		events = kept
	}
	media.Events = events
	for i := range media.Events {
		media.Events[i].Seq = i
	}
	return media, nil
}
