package demux

import (
	"bytes"
	"fmt"

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

// BoxTypes lists the top-level box types of data.  It exists for tests and
// diagnostics, not for parsing decisions.
func BoxTypes(data []byte) ([]string, error) {
	top, _, err := parseBoxes(data, 0, int64(len(data)), "mp4", false)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(top))
	for i, b := range top {
		out[i] = fmt.Sprintf("%s@%d+%d", b.typ, b.off, b.size)
	}
	return out, nil
}

func findTop(boxes []*rawBox, typ string) *rawBox {
	for _, b := range boxes {
		if b.typ == typ {
			return b
		}
	}
	return nil
}

func findTops(boxes []*rawBox, typ string) []*rawBox {
	var out []*rawBox
	for _, b := range boxes {
		if b.typ == typ {
			out = append(out, b)
		}
	}
	return out
}

func hasIndex(top []*rawBox) bool {
	if findTop(top, "sidx") != nil {
		return true
	}
	mfra := findTop(top, "mfra")
	if mfra == nil {
		return false
	}
	return mfra.find("tfra") != nil
}

// trimSamplesBefore drops the samples that lie beyond the first cut byte.  A
// truncated input keeps its verified prefix; the incomplete range is reported
// rather than silently dropped.
func trimSamplesBefore(tracks []Track, cutStart int64) error {
	for ti := range tracks {
		tr := &tracks[ti]
		keep := 0
		for _, s := range tr.Samples {
			if s.Offset+int64(s.Size) > cutStart {
				break
			}
			keep++
		}
		tr.Samples = tr.Samples[:keep]
	}
	return nil
}

// movieTimescaleOf returns the mvhd timescale.
func movieTimescaleOf(moov *rawBox) uint32 {
	mvhd := moov.find("mvhd")
	if mvhd == nil {
		return 0
	}
	r := newBinReader(mvhd, fault.OpBoxHeader)
	version, _, err := r.fullBox()
	if err != nil {
		return 0
	}
	skip := 8
	if version == 1 {
		skip = 16
	}
	if err := r.skip(skip); err != nil {
		return 0
	}
	ts, err := r.u32()
	if err != nil {
		return 0
	}
	return ts
}
