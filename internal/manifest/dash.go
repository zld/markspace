package manifest

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"

	"mediapipe/internal/clock"
)

// Minimal DASH MPD model: one Representation with a SegmentTemplate and an
// explicit SegmentTimeline. Gap policy: when an S entry starts after the
// running cursor, the hole is filled with explicit Missing refs of the
// template duration so the timeline is never silently shortened.

type mpdXML struct {
	XMLName xml.Name `xml:"MPD"`
	Period  struct {
		Adaptation struct {
			Representation struct {
				ID       string `xml:"id,attr"`
				BaseURL  string `xml:"BaseURL"`
				Template struct {
					Timescale   int64  `xml:"timescale,attr"`
					Duration    int64  `xml:"duration,attr"`
					Media       string `xml:"media,attr"`
					StartNumber int64  `xml:"startNumber,attr"`
					Timeline    struct {
						S []struct {
							T *int64 `xml:"t,attr"`
							D int64  `xml:"d,attr"`
							R int64  `xml:"r,attr"`
						} `xml:"S"`
					} `xml:"SegmentTimeline"`
				} `xml:"SegmentTemplate"`
			} `xml:"Representation"`
		} `xml:"AdaptationSet"`
	} `xml:"Period"`
}

// ParseDASH parses a static MPD into ordered segment references.
func ParseDASH(track string, r io.Reader) (*Playlist, error) {
	fail := func(op string, err error) (*Playlist, error) {
		return nil, &clock.StageError{Track: track, Stage: clock.StageManifest, Op: op, Err: err}
	}
	var m mpdXML
	if err := xml.NewDecoder(r).Decode(&m); err != nil {
		return fail("xml", err)
	}
	rep := m.Period.Adaptation.Representation
	tpl := rep.Template
	if tpl.Timescale <= 0 {
		return fail("timescale", fmt.Errorf("non-positive timescale %d", tpl.Timescale))
	}
	if tpl.Media == "" {
		return fail("media", fmt.Errorf("missing media template"))
	}
	if !strings.Contains(tpl.Media, "$Number$") {
		return fail("media", fmt.Errorf("media template %q lacks $Number$", tpl.Media))
	}
	if err := ValidateURI(tpl.Media); err != nil {
		return fail("media", err)
	}
	if rep.BaseURL != "" {
		if err := ValidateURI(rep.BaseURL); err != nil {
			return fail("baseurl", err)
		}
	}
	if len(tpl.Timeline.S) == 0 {
		return fail("timeline", fmt.Errorf("empty SegmentTimeline"))
	}

	var refs []SegmentRef
	var cursor clock.Ticks
	num := tpl.StartNumber
	seen := map[int64]bool{}
	for si, s := range tpl.Timeline.S {
		if s.D <= 0 {
			return fail("duration", fmt.Errorf("S[%d]: non-positive duration %d", si, s.D))
		}
		repeat := s.R
		if repeat < 0 {
			return fail("repeat", fmt.Errorf("S[%d]: negative repeat %d", si, s.R))
		}
		dur := clock.Ticks(s.D * clock.Rate / tpl.Timescale)
		if dur <= 0 {
			return fail("duration", fmt.Errorf("S[%d]: duration rounds to zero ticks", si))
		}
		t := cursor
		if s.T != nil {
			t = clock.Ticks(*s.T * clock.Rate / tpl.Timescale)
		}
		if t < cursor {
			return fail("timeline", fmt.Errorf("S[%d]: backward timestamp %d < %d", si, t, cursor))
		}
		for t > cursor { // explicit gap -> missing slots, timeline preserved
			refs = append(refs, SegmentRef{
				TrackID:  track,
				Index:    -1,
				Interval: clock.Interval{Start: cursor, End: min64(t, cursor+dur)},
				Missing:  true,
			})
			cursor = refs[len(refs)-1].Interval.End
		}
		for k := int64(0); k <= repeat; k++ {
			if seen[num] {
				return fail("duplicate", fmt.Errorf("duplicate segment number %d", num))
			}
			seen[num] = true
			uri := strings.ReplaceAll(tpl.Media, "$Number$", strconv.FormatInt(num, 10))
			if rep.BaseURL != "" {
				uri = strings.TrimSuffix(rep.BaseURL, "/") + "/" + uri
			}
			refs = append(refs, SegmentRef{
				TrackID:  track,
				Index:    num,
				URI:      uri,
				Interval: clock.Interval{Start: cursor, End: cursor + dur},
			})
			cursor += dur
			num++
		}
	}
	miss := int64(-1)
	for i := range refs {
		if refs[i].Missing {
			refs[i].Index = miss
			miss--
		}
	}
	return &Playlist{TrackID: track, Refs: refs, Complete: true}, nil
}

func min64(a, b clock.Ticks) clock.Ticks {
	if a < b {
		return a
	}
	return b
}
