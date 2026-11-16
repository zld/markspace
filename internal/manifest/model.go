// Package manifest defines the MPD object model produced by the packager.
// The structs carry encoding/xml tags for stable serialization in mpd.Write.
package manifest

import (
	"encoding/xml"
	"fmt"

	"dashgen/internal/align"
	"dashgen/internal/timebase"
)

// Namespace and profile identifiers written into every generated MPD.
const (
	DASHNamespace = "urn:mpeg:dash:schema:mpd:2011"
	MainProfile   = "urn:mpeg:dash:profile:isoff-main:2011"
	VideoMimeType = "video/mp4"
)

// MPD is the document root.
type MPD struct {
	XMLName                   xml.Name `xml:"urn:mpeg:dash:schema:mpd:2011 MPD"`
	Profiles                  string   `xml:"profiles,attr"`
	Type                      string   `xml:"type,attr"`
	MediaPresentationDuration string   `xml:"mediaPresentationDuration,attr"`
	MinBufferTime             string   `xml:"minBufferTime,attr"`
	Periods                   []Period `xml:"Period"`
}

// Period groups adaptation sets sharing one time window.
type Period struct {
	ID             string          `xml:"id,attr"`
	Start          string          `xml:"start,attr,omitempty"`
	AdaptationSets []AdaptationSet `xml:"AdaptationSet"`
}

// AdaptationSet groups interchangeable representations.
type AdaptationSet struct {
	ID              int              `xml:"id,attr"`
	ContentType     string           `xml:"contentType,attr"`
	MimeType        string           `xml:"mimeType,attr"`
	SegmentAligned  bool             `xml:"segmentAlignment,attr"`
	Representations []Representation `xml:"Representation"`
}

// Representation is one rendition with its own timescale and segment template.
type Representation struct {
	ID              string           `xml:"id,attr"`
	Bandwidth       int64            `xml:"bandwidth,attr"`
	SegmentTemplate *SegmentTemplate `xml:"SegmentTemplate,omitempty"`
}

// SegmentTemplate carries the media naming pattern and compressed timeline.
type SegmentTemplate struct {
	Timescale       int64            `xml:"timescale,attr"`
	Media           string           `xml:"media,attr"`
	Initialization  string           `xml:"initialization,attr,omitempty"`
	StartNumber     int64            `xml:"startNumber,attr"`
	SegmentTimeline *SegmentTimeline `xml:"SegmentTimeline,omitempty"`
}

// SegmentTimeline is the ordered list of compressed S entries.
type SegmentTimeline struct {
	Entries []TimelineEntry `xml:"S"`
}

// TimelineEntry is one S element. Repeated entries sharing the start implied
// by the previous entry's duration are represented with Repeat; any gap or
// discontinuity forces an explicit Time.
type TimelineEntry struct {
	Time     *int64 `xml:"t,attr,omitempty"`
	Duration int64  `xml:"d,attr"`
	Repeat   int64  `xml:"r,attr,omitempty"`
}

// BuildTimeline compresses native-tick bounds into S entries. A new entry with
// an explicit t is started for the first segment, for any gap/jump, and for a
// duration change; contiguous equal-duration segments collapse into r repeats.
func BuildTimeline(bounds []align.SegmentBounds) (*SegmentTimeline, error) {
	if len(bounds) == 0 {
		return nil, fmt.Errorf("manifest: cannot build timeline without segments")
	}
	tl := &SegmentTimeline{}
	var (
		entries []TimelineEntry
		prevDur int64
	)
	for i, b := range bounds {
		if b.DurationTicks <= 0 {
			return nil, fmt.Errorf("manifest: segment %d has non-positive duration", b.Number)
		}
		if _, ok := timebase.Add64(b.StartTicks, b.DurationTicks); !ok {
			return nil, fmt.Errorf("manifest: segment %d end overflows int64", b.Number)
		}
		// A new explicit-time entry is needed for the first segment, any
		// gap/jump (Gap means start != previous end), or a duration change.
		explicitTime := i == 0 || b.Gap
		durationChange := i > 0 && b.DurationTicks != prevDur
		if explicitTime || durationChange {
			t := b.StartTicks
			entries = append(entries, TimelineEntry{Time: &t, Duration: b.DurationTicks})
		} else {
			entries[len(entries)-1].Repeat++
		}
		prevDur = b.DurationTicks
	}
	tl.Entries = entries
	return tl, nil
}

// TimelineSegmentCount expands r repeats back into the number of segments.
func TimelineSegmentCount(tl *SegmentTimeline) int64 {
	var n int64
	for _, e := range tl.Entries {
		n += e.Repeat + 1
	}
	return n
}

// TimelineTick is one expanded segment in native timescale ticks.
type TimelineTick struct {
	Start    int64
	Duration int64
}

// ExpandTimeline reverses BuildTimeline: every S entry yields Repeat+1
// segments starting at t, t+d, t+2d, ... . It returns an error on a missing
// explicit time or integer overflow.
func ExpandTimeline(tl *SegmentTimeline) ([]TimelineTick, error) {
	if tl == nil {
		return nil, fmt.Errorf("manifest: missing SegmentTimeline")
	}
	var out []TimelineTick
	for ei, e := range tl.Entries {
		if e.Time == nil {
			return nil, fmt.Errorf("manifest: S entry %d has no explicit t", ei)
		}
		if e.Duration <= 0 {
			return nil, fmt.Errorf("manifest: S entry %d has non-positive duration", ei)
		}
		start := *e.Time
		for k := int64(0); k <= e.Repeat; k++ {
			if k > 0 {
				next, ok := timebase.Add64(start, e.Duration)
				if !ok {
					return nil, fmt.Errorf("manifest: timeline start overflows at entry %d", ei)
				}
				start = next
			}
			if _, ok := timebase.Add64(start, e.Duration); !ok {
				return nil, fmt.Errorf("manifest: timeline end overflows at entry %d", ei)
			}
			out = append(out, TimelineTick{Start: start, Duration: e.Duration})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("manifest: empty SegmentTimeline")
	}
	return out, nil
}

// FormatDuration renders an exact millisecond rational as an ISO-8601
// duration of the form PT12.500S (no hours/minutes split needed for clips).
func FormatDuration(ms timebase.Rational) string {
	secs, err := ms.Div(1000)
	if err != nil {
		secs = ms
	}
	return "PT" + timebase.FormatFixed(secs, 3) + "S"
}
