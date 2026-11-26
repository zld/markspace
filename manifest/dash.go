package manifest

import (
	"encoding/xml"
	"fmt"
	"strings"

	"streamcheck/core"
)

// Static-segmented DASH only: SegmentTemplate with SegmentTimeline.
type sElem struct {
	T *int64 `xml:"t,attr"`
	D int64  `xml:"d,attr"`
	R int64  `xml:"r,attr"`
}

type segTemplate struct {
	Timescale   int64  `xml:"timescale,attr"`
	StartNumber int64  `xml:"startNumber,attr"`
	Media       string `xml:"media,attr"`
	Timeline    *struct {
		S []sElem `xml:"S"`
	} `xml:"SegmentTimeline"`
}

type mpdXML struct {
	XMLName xml.Name `xml:"MPD"`
	Periods []struct {
		Sets []struct {
			Template *segTemplate `xml:"SegmentTemplate"`
			Reps     []struct {
				ID        string       `xml:"id,attr"`
				Bandwidth int64        `xml:"bandwidth,attr"`
				Template  *segTemplate `xml:"SegmentTemplate"`
			} `xml:"Representation"`
		} `xml:"AdaptationSet"`
	} `xml:"Period"`
}

// ParseDASH parses a static MPD using SegmentTemplate/SegmentTimeline into
// the normalized Timeline. Segment provenance (representation, index, URI)
// is preserved.
func ParseDASH(data []byte, source string) (*Timeline, error) {
	var doc mpdXML
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, core.E(core.KindManifest, "parse-mpd", "invalid XML", err).At(source, -1)
	}
	tl := &Timeline{Source: "dash"}
	for _, p := range doc.Periods {
		for _, set := range p.Sets {
			for _, r := range set.Reps {
				tpl := r.Template
				if tpl == nil {
					tpl = set.Template
				}
				if tpl == nil || tpl.Timeline == nil {
					return nil, core.E(core.KindManifest, "parse-mpd",
						fmt.Sprintf("representation %q: no SegmentTemplate/SegmentTimeline", r.ID), nil).At(source, -1)
				}
				rep, err := buildRep(r.ID, r.Bandwidth, tpl)
				if err != nil {
					return nil, err.At(source, -1)
				}
				tl.Reps = append(tl.Reps, rep)
			}
		}
	}
	if len(tl.Reps) == 0 {
		return nil, core.E(core.KindManifest, "parse-mpd", "no representations", nil).At(source, -1)
	}
	return tl, nil
}

func buildRep(id string, bw int64, tpl *segTemplate) (Representation, *core.Error) {
	rep := Representation{ID: id, Bandwidth: bw}
	ts := tpl.Timescale
	if ts <= 0 {
		ts = 1
	}
	num := tpl.StartNumber
	if num == 0 {
		num = 1
	}
	var cur core.Rat // next expected start, in timescale units
	for si, s := range tpl.Timeline.S {
		if s.D <= 0 {
			return rep, core.E(core.KindManifest, "parse-mpd",
				fmt.Sprintf("representation %q: S[%d] non-positive duration", id, si), nil)
		}
		if s.R < 0 {
			return rep, core.E(core.KindManifest, "parse-mpd",
				fmt.Sprintf("representation %q: negative repeat unsupported for static segments", id), nil)
		}
		start := cur
		if s.T != nil {
			start = core.NewRat(*s.T, ts)
		}
		dur := core.NewRat(s.D, ts)
		for k := int64(0); k <= s.R; k++ {
			off := core.NewRat(k*s.D, ts)
			rep.Segments = append(rep.Segments, Segment{
				Index: int(num),
				Start: start.Add(off),
				Dur:   dur,
				URI:   strings.ReplaceAll(tpl.Media, "$Number$", fmt.Sprint(num)),
			})
			num++
		}
		cur = start.Add(core.NewRat((s.R+1)*s.D, ts))
	}
	return rep, nil
}
