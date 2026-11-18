// Package mpd builds the static MPEG-DASH MPD document model from the shared
// normalized manifest and serializes it deterministically.
package mpd

import (
	"encoding/xml"
	"strconv"

	"dashforge/internal/model"
)

// Document is the root MPD element. Field order below defines the emitted
// attribute order; encoding/xml renders struct fields in declaration order,
// which is what makes the output reproducible.
type Document struct {
	XMLName               xml.Name `xml:"urn:mpeg:dash:schema:mpd:2011 MPD"`
	Type                  string   `xml:"type,attr"`
	Profile               string   `xml:"profiles,attr"`
	MinBufferTime         string   `xml:"minBufferTime,attr"`
	MediaPresentationTime string   `xml:"mediaPresentationDuration,attr"`
	Period                Period   `xml:"Period"`
}

// Period holds one adaptation set containing all video representations.
type Period struct {
	ID            string        `xml:"id,attr"`
	Start         string        `xml:"start,attr"`
	AdaptationSet AdaptationSet `xml:"AdaptationSet"`
}

// AdaptationSet carries the shared SegmentTemplate. Every representation
// inherits it and only overrides per-rendition attributes.
type AdaptationSet struct {
	ContentType     string           `xml:"contentType,attr"`
	SegmentTemplate SegmentTemplate  `xml:"SegmentTemplate"`
	Representations []Representation `xml:"Representation"`
}

// SegmentTemplate uses a $Time$-based SegmentTimeline so non-uniform fragment
// durations and short final fragments are expressed exactly.
type SegmentTemplate struct {
	Timescale       int             `xml:"timescale,attr"`
	Media           string          `xml:"media,attr"`
	Initialization  string          `xml:"initialization,attr,omitempty"`
	SegmentTimeline SegmentTimeline `xml:"SegmentTimeline"`
}

// SegmentTimeline wraps the S run list. The wrapper type exists so the field
// name matches the XML element name for unmarshaling (encoding/xml matches on
// the Go field name when no explicit name is given).
type SegmentTimeline struct {
	Entries []S `xml:"S"`
}

// S is one SegmentTimeline run; Repeat 0 is omitted for determinism.
type S struct {
	Time     int64 `xml:"t,attr"`
	Duration int64 `xml:"d,attr"`
	Repeat   int   `xml:"r,attr,omitempty"`
}

// Representation is one bitrate rung with its own template paths.
type Representation struct {
	ID             string `xml:"id,attr"`
	Bandwidth      int    `xml:"bandwidth,attr"`
	Width          int    `xml:"width,attr"`
	Height         int    `xml:"height,attr"`
	FrameRate      string `xml:"frameRate,attr"`
	Codecs         string `xml:"codecs,attr"`
	Media          string `xml:"media,attr"`
	Initialization string `xml:"initialization,attr,omitempty"`
}

const profileURN = "urn:mpeg:dash:profile:isoff-on-demand:2011"

// Build converts the normalized manifest into an MPD document. Renditions
// arrive already sorted by bandwidth; their order is preserved verbatim.
func Build(man *model.Manifest) *Document {
	doc := &Document{
		Type:                  "static",
		Profile:               profileURN,
		MinBufferTime:         "PT1.5S",
		MediaPresentationTime: FormatDuration(man.PeriodDuration(), man.Timescale),
		Period: Period{
			ID:    "P0",
			Start: "PT0S",
			AdaptationSet: AdaptationSet{
				ContentType:     "video",
				Representations: make([]Representation, 0, len(man.Renditions)),
			},
		},
	}
	as := &doc.Period.AdaptationSet

	for _, rm := range man.Renditions {
		// The first rendition seeds the shared template; all renditions share
		// one timescale and timeline semantics per adaptation set. The S runs
		// are derived directly from the fragment list so the document never
		// depends on a separately maintained, possibly stale timeline field.
		if len(as.Representations) == 0 {
			as.SegmentTemplate = SegmentTemplate{
				Timescale:       rm.Timescale,
				Media:           rm.Media,
				Initialization:  rm.Initialization,
				SegmentTimeline: SegmentTimeline{Entries: segmentsToS(rm.Segments)},
			}
		}
		as.Representations = append(as.Representations, Representation{
			ID:             rm.ID,
			Bandwidth:      rm.Bandwidth,
			Width:          rm.Width,
			Height:         rm.Height,
			FrameRate:      rm.FrameRate,
			Codecs:         rm.Codec,
			Media:          rm.Media,
			Initialization: rm.Initialization,
		})
	}
	return doc
}

// segmentsToS coalesces contiguous, equal-duration fragments into S runs.
func segmentsToS(segs []model.SegmentInfo) []S {
	var out []S
	for _, seg := range segs {
		if n := len(out); n > 0 {
			last := &out[n-1]
			expected := last.Time + last.Duration*int64(last.Repeat+1)
			if seg.Start == expected && seg.Duration == last.Duration {
				last.Repeat++
				continue
			}
		}
		out = append(out, S{Time: seg.Start, Duration: seg.Duration})
	}
	return out
}

// FormatDuration renders ticks on a timescale as an ISO 8601 duration using a
// fractional-second component, e.g. 540000 ticks at 90000 -> "PT6S".
func FormatDuration(ticks int64, timescale int) string {
	if ticks <= 0 || timescale <= 0 {
		return "PT0S"
	}
	whole := ticks / int64(timescale)
	rem := ticks % int64(timescale)
	hours := whole / 3600
	minutes := (whole % 3600) / 60
	seconds := whole % 60

	var b []byte
	b = append(b, 'P', 'T')
	if hours > 0 {
		b = strconv.AppendInt(b, hours, 10)
		b = append(b, 'H')
	}
	if minutes > 0 {
		b = strconv.AppendInt(b, minutes, 10)
		b = append(b, 'M')
	}
	if rem == 0 {
		b = strconv.AppendInt(b, seconds, 10)
		b = append(b, 'S')
		return string(b)
	}
	// Fractional component computed with integer arithmetic only: print
	// rem/timescale as a decimal and strip trailing zeros so equal durations
	// always render identically (e.g. 1/2 -> "0.5", 723/1000 -> "0.723").
	num, den := rem, int64(timescale)
	var frac []byte
	for num > 0 {
		num *= 10
		frac = append(frac, byte('0'+num/den))
		num %= den
	}
	for len(frac) > 0 && frac[len(frac)-1] == '0' {
		frac = frac[:len(frac)-1]
	}
	b = strconv.AppendInt(b, seconds, 10)
	b = append(b, '.')
	b = append(b, frac...)
	b = append(b, 'S')
	return string(b)
}
