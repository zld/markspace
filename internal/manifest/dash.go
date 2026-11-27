package manifest

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mediacheck/internal/timeline"
)

type mpdXML struct {
	XMLName xml.Name `xml:"MPD"`
	Periods []struct {
		Adaptations []struct {
			Reps []struct {
				ID       string `xml:"id,attr"`
				Template *struct {
					Timescale      uint64 `xml:"timescale,attr"`
					Initialization string `xml:"initialization,attr"`
					Media          string `xml:"media,attr"`
					StartNumber    int64  `xml:"startNumber,attr"`
					Timeline       *struct {
						S []struct {
							T *uint64 `xml:"t,attr"`
							D uint64  `xml:"d,attr"`
							R int     `xml:"r,attr"`
						} `xml:"S"`
					} `xml:"SegmentTimeline"`
				} `xml:"SegmentTemplate"`
			} `xml:"Representation"`
		} `xml:"AdaptationSet"`
	} `xml:"Period"`
}

// ParseDASH reads a DASH MPD into segment events, resolving Representation,
// initialization segment, timescale, start time and per-segment durations
// from SegmentTemplate/SegmentTimeline.
func ParseDASH(path string) ([]Event, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, timeline.New(timeline.KindMissingRecord, "manifest.dash", path, 0, "open: %v", err)
	}
	var doc mpdXML
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, timeline.New(timeline.KindMissingRecord, "manifest.dash", path, 0, "xml: %v", err)
	}
	dir := filepath.Dir(path)
	var events []Event
	for _, p := range doc.Periods {
		for _, a := range p.Adaptations {
			for _, r := range a.Reps {
				t := r.Template
				if t == nil {
					continue
				}
				if t.Timescale == 0 {
					return nil, timeline.New(timeline.KindZeroTimescale, "manifest.dash", path, 0,
						"representation %q has zero timescale", r.ID)
				}
				if t.StartNumber < 0 {
					return nil, timeline.New(timeline.KindBadSequence, "manifest.dash", path, 0,
						"representation %q has negative startNumber %d", r.ID, t.StartNumber)
				}
				if t.Timeline == nil || len(t.Timeline.S) == 0 {
					return nil, timeline.New(timeline.KindMissingDuration, "manifest.dash", path, 0,
						"representation %q has no SegmentTimeline", r.ID)
				}
				num := t.StartNumber
				var cur uint64
				curSet := false
				for _, s := range t.Timeline.S {
					if s.D == 0 {
						return nil, timeline.New(timeline.KindMissingDuration, "manifest.dash", path, 0,
							"representation %q has S with zero duration", r.ID)
					}
					if s.T != nil {
						cur, curSet = *s.T, true
					}
					if !curSet {
						return nil, timeline.New(timeline.KindMissingDuration, "manifest.dash", path, 0,
							"representation %q first S has no start time t", r.ID)
					}
					repeat := s.R
					if repeat < 0 {
						repeat = 0
					}
					for i := 0; i <= repeat; i++ {
						seg := strings.ReplaceAll(t.Media, "$Number$", strconv.FormatInt(num, 10))
						events = append(events, Event{
							ManifestPath: path, Format: "dash",
							Representation: r.ID,
							Seq:            num,
							RawTime:        cur,
							RawDuration:    s.D,
							Timescale:      t.Timescale,
							SegmentPath:    filepath.Join(dir, seg),
						})
						num++
						cur += s.D
					}
				}
			}
		}
	}
	return events, nil
}
