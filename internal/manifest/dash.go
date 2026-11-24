// Package manifest parses DASH MPDs and HLS playlists into a unified
// integer-tick timeline. All validation errors carry the representation,
// the entry index and the raw byte offset inside the manifest.
package manifest

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"

	"mediarun/internal/clock"
	"mediarun/internal/fault"
)

// SegmentRef is one media segment of a DASH representation.
type SegmentRef struct {
	Number        int64       // segment sequence number
	DurationUnits int64       // duration in representation timescale units
	Duration      clock.Ticks // duration on the master timeline
	Media         string      // resolved media URL (relative to MPD)
	Offset        int64       // raw byte offset in the MPD where defined
}

// Representation is one DASH representation layer.
type Representation struct {
	ID        string
	Bandwidth int64
	Timescale int64
	Init      string // resolved initialization segment URL
	Segments  []SegmentRef
}

// MPD is a parsed DASH manifest.
type MPD struct {
	Path            string
	Representations []*Representation
}

// ParseMPD parses a DASH MPD using SegmentTemplate/SegmentTimeline and
// validates timescale, initialization segment, segment numbering and
// durations for every representation.
func ParseMPD(path string, data []byte) (*MPD, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	mpd := &MPD{Path: path}
	var cur *Representation
	var tmpl *segTemplate
	var inTimeline bool
	for {
		tok, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, fault.New(Stage(), "malformed XML").At(path, int64(dec.InputOffset())).Wrap(err)
		}
		off := int64(dec.InputOffset())
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "Representation":
				cur = &Representation{Timescale: -1}
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "id":
						cur.ID = a.Value
					case "bandwidth":
						fmt.Sscanf(a.Value, "%d", &cur.Bandwidth)
					}
				}
				mpd.Representations = append(mpd.Representations, cur)
			case "SegmentTemplate":
				tmpl = &segTemplate{startNumber: 1, timescale: -1, offset: off}
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "timescale":
						fmt.Sscanf(a.Value, "%d", &tmpl.timescale)
					case "duration":
						fmt.Sscanf(a.Value, "%d", &tmpl.duration)
					case "startNumber":
						fmt.Sscanf(a.Value, "%d", &tmpl.startNumber)
					case "media":
						tmpl.media = a.Value
					case "initialization":
						tmpl.init = a.Value
					}
				}
			case "SegmentTimeline":
				inTimeline = true
			case "S":
				if inTimeline && tmpl != nil {
					s := sEntry{d: -1, offset: off}
					for _, a := range t.Attr {
						switch a.Name.Local {
						case "t":
							fmt.Sscanf(a.Value, "%d", &s.t)
						case "d":
							fmt.Sscanf(a.Value, "%d", &s.d)
						case "r":
							fmt.Sscanf(a.Value, "%d", &s.r)
						}
					}
					tmpl.entries = append(tmpl.entries, s)
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "SegmentTimeline":
				inTimeline = false
			case "SegmentTemplate":
				if cur != nil && tmpl != nil {
					if err := applyTemplate(cur, tmpl, path); err != nil {
						return nil, err
					}
				}
				tmpl = nil
			case "Representation":
				cur = nil
			}
		}
	}
	if len(mpd.Representations) == 0 {
		return nil, fault.New(Stage(), "MPD has no Representation").At(path, 0)
	}
	for _, r := range mpd.Representations {
		if len(r.Segments) == 0 {
			return nil, fault.New(Stage(), "representation has no segments").At(path, 0).For(r.ID, -1)
		}
	}
	return mpd, nil
}

// Stage returns the fault stage for manifest errors (exposed for tests).
func Stage() fault.Stage { return fault.StageManifest }

type segTemplate struct {
	timescale   int64
	duration    int64
	startNumber int64
	media, init string
	offset      int64
	entries     []sEntry
}

type sEntry struct {
	t, d, r int64
	offset  int64
}

// applyTemplate expands a SegmentTemplate into validated segments on cur.
func applyTemplate(cur *Representation, tmpl *segTemplate, path string) error {
	if tmpl.timescale <= 0 {
		return fault.New(Stage(), fmt.Sprintf("invalid timescale %d", tmpl.timescale)).
			At(path, tmpl.offset).For(cur.ID, len(cur.Segments))
	}
	cur.Timescale = tmpl.timescale
	if tmpl.init == "" {
		return fault.New(Stage(), "missing initialization segment template").
			At(path, tmpl.offset).For(cur.ID, -1)
	}
	cur.Init = fillTemplate(tmpl.init, cur.ID, tmpl.startNumber)
	if len(tmpl.entries) == 0 {
		return fault.New(Stage(), "SegmentTemplate without SegmentTimeline is unsupported").
			At(path, tmpl.offset).For(cur.ID, -1)
	}
	num := tmpl.startNumber
	entryIdx := 0
	for _, s := range tmpl.entries {
		if s.d <= 0 {
			return fault.New(Stage(), fmt.Sprintf("invalid segment duration %d", s.d)).
				At(path, s.offset).For(cur.ID, entryIdx)
		}
		for i := int64(0); i <= s.r; i++ {
			dur, err := clock.ToMaster(s.d, tmpl.timescale)
			if err != nil {
				return fault.New(Stage(), "duration conversion failed").
					At(path, s.offset).For(cur.ID, entryIdx).Wrap(err)
			}
			cur.Segments = append(cur.Segments, SegmentRef{
				Number:        num,
				DurationUnits: s.d,
				Duration:      dur,
				Media:         fillTemplate(tmpl.media, cur.ID, num),
				Offset:        s.offset,
			})
			num++
			entryIdx++
		}
	}
	// Segment numbers must be strictly increasing (they are by
	// construction here; the check guards future list-based sources).
	for i := 1; i < len(cur.Segments); i++ {
		if cur.Segments[i].Number <= cur.Segments[i-1].Number {
			return fault.New(Stage(), "segment numbers not increasing").
				At(path, cur.Segments[i].Offset).For(cur.ID, i)
		}
	}
	return nil
}

// fillTemplate expands $RepresentationID$ and $Number$ in a URL template.
func fillTemplate(tmpl, repID string, number int64) string {
	s := strings.ReplaceAll(tmpl, "$RepresentationID$", repID)
	s = strings.ReplaceAll(s, "$Number$", fmt.Sprintf("%d", number))
	return s
}
