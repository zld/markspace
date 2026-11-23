// Package manifest parses upstream DASH MPDs and HLS media playlists into a
// common, master-clock representation and enforces structural validation:
// zero timescales, out-of-range durations and duplicate segment references
// are rejected rather than repaired silently.
package manifest

import (
	"encoding/xml"
	"io"
	"strconv"
	"strings"

	"mediakernel/internal/clock"
	"mediakernel/internal/mediaerr"
)

// SegmentRef is one fragment reference on a representation timeline.
type SegmentRef struct {
	Index        uint32 `json:"index"`      // zero-based fragment ordinal
	URL          string `json:"url"`        // source reference
	StartTick    uint64 `json:"start_tick"` // master 90 kHz
	DurationTick uint64 `json:"duration_tick"`
	Timescale    uint64 `json:"timescale"` // native scale, retained for audit
}

// Representation is one DASH adaptation-set member.
type Representation struct {
	ID           string       `json:"id"`
	Kind         string       `json:"kind"` // video | audio
	Codecs       string       `json:"codecs"`
	BandwidthBps uint64       `json:"bandwidth_bps"`
	Rank         int          `json:"rank"` // stable ordering number
	Timescale    uint64       `json:"timescale"`
	Segments     []SegmentRef `json:"segments"`
	StartTick    uint64       `json:"start_tick"`
	EndTick      uint64       `json:"end_tick"`
}

// DASHDoc is the validated parse result.
type DASHDoc struct {
	MediaPresentationDuration uint64           `json:"duration_tick"`
	Representations           []Representation `json:"representations"`
}

// --- minimal MPD XML subset (SegmentTemplate + SegmentTimeline) ---

type xmlMPD struct {
	XMLName        xml.Name      `xml:"MPD"`
	Duration       string        `xml:"mediaPresentationDuration,attr"`
	AdaptationSets []xmlAdaptSet `xml:"Period>AdaptationSet"`
}

type xmlAdaptSet struct {
	ContentType     string              `xml:"contentType,attr"`
	SegmentTemplate xmlSegmentTemplate  `xml:"SegmentTemplate"`
	Representations []xmlRepresentation `xml:"Representation"`
}

type xmlSegmentTemplate struct {
	Timescale      uint64             `xml:"timescale,attr"`
	Initialization string             `xml:"initialization,attr"`
	Media          string             `xml:"media,attr"`
	Timeline       []xmlTimelineEntry `xml:"SegmentTimeline>S"`
}

type xmlTimelineEntry struct {
	T uint64 `xml:"t,attr"`
	D uint64 `xml:"d,attr"`
	R int    `xml:"r,attr"` // repeat count, -1 unsupported (must be explicit)
}

type xmlRepresentation struct {
	ID        string `xml:"id,attr"`
	Bandwidth uint64 `xml:"bandwidth,attr"`
	Codecs    string `xml:"codecs,attr"`
}

// ParseDASH reads and validates an MPD. All native timestamps are converted to
// the master clock with overflow failure.
func ParseDASH(r io.Reader) (*DASHDoc, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, mediaerr.New(mediaerr.CatManifest, "read", "cannot read MPD").Wrap(err)
	}
	var x xmlMPD
	if err := xml.Unmarshal(raw, &x); err != nil {
		return nil, mediaerr.New(mediaerr.CatManifest, "xml", "MPD is not well-formed").Wrap(mediaerr.ErrMalformed)
	}
	if len(x.AdaptationSets) == 0 {
		return nil, mediaerr.New(mediaerr.CatManifest, "no_adaptation", "MPD has no adaptation set")
	}

	doc := &DASHDoc{}
	dur, err := parseISODuration(x.Duration)
	if err != nil {
		return nil, err
	}
	if dur > mediaerr.MaxDurationTick {
		return nil, mediaerr.New(mediaerr.CatValidation, "duration_cap", "presentation exceeds duration limit")
	}
	doc.MediaPresentationDuration = dur

	rank := 0
	for _, as := range x.AdaptationSets {
		kind := strings.ToLower(as.ContentType)
		if kind != "video" && kind != "audio" {
			return nil, mediaerr.New(mediaerr.CatManifest, "content_type", "unsupported content type: "+as.ContentType)
		}
		tpl := as.SegmentTemplate
		if tpl.Timescale == 0 {
			return nil, mediaerr.New(mediaerr.CatTimebase, "zero_timescale",
				"timescale must be positive").With(kind, 0, false)
		}
		if tpl.Media == "" || len(tpl.Timeline) == 0 {
			return nil, mediaerr.New(mediaerr.CatManifest, "no_template", "segment template/timeline missing")
		}
		if len(as.Representations) == 0 {
			return nil, mediaerr.New(mediaerr.CatManifest, "no_rep", "adaptation set has no representation")
		}
		if len(doc.Representations)+len(as.Representations) > mediaerr.MaxRepresentations {
			return nil, mediaerr.New(mediaerr.CatValidation, "rep_cap", "too many representations")
		}
		sc, err := clock.NewScale(tpl.Timescale, 1)
		if err != nil {
			return nil, mediaerr.New(mediaerr.CatTimebase, "bad_timescale", err.Error())
		}
		for _, xr := range as.Representations {
			rep, err := buildRepresentation(kind, xr, tpl, sc)
			if err != nil {
				return nil, err
			}
			rep.Rank = rank
			rank++
			if rep.EndTick > dur {
				return nil, mediaerr.New(mediaerr.CatValidation, "segment_out_of_range",
					"segment extends past mediaPresentationDuration").
					With(rep.ID, rep.EndTick, false)
			}
			doc.Representations = append(doc.Representations, rep)
		}
	}
	return doc, nil
}

func buildRepresentation(kind string, xr xmlRepresentation, tpl xmlSegmentTemplate,
	sc clock.Scale) (Representation, error) {
	if xr.ID == "" {
		return Representation{}, mediaerr.New(mediaerr.CatManifest, "rep_id", "representation id missing")
	}
	if !codecAllowed(kind, xr.Codecs) {
		return Representation{}, mediaerr.New(mediaerr.CatManifest, "codec",
			"first version supports H.264/AAC only, got "+xr.Codecs).With(xr.ID, 0, false)
	}
	rep := Representation{
		ID: xr.ID, Kind: kind, Codecs: xr.Codecs,
		BandwidthBps: xr.Bandwidth, Timescale: tpl.Timescale,
	}
	seenURL := map[string]uint32{}
	idx := uint32(0)
	var cursor uint64
	var err error
	for _, e := range tpl.Timeline {
		if e.R < 0 {
			return Representation{}, mediaerr.New(mediaerr.CatManifest, "timeline_r",
				"indefinite repeat (r=-1) is not supported").With(xr.ID, 0, false)
		}
		startNative := e.T
		if idx > 0 && e.T == 0 {
			startNative = cursor // timeline omits T on continuation entries
		}
		if _, err := sc.ToMaster(startNative); err != nil {
			return Representation{}, mediaerr.New(mediaerr.CatTimebase, "tick_overflow", err.Error()).
				With(xr.ID, 0, false)
		}
		for k := 0; k <= e.R; k++ {
			if idx >= mediaerr.MaxSegments {
				return Representation{}, mediaerr.New(mediaerr.CatValidation, "segment_cap",
					"segment count exceeds limit").With(xr.ID, 0, false)
			}
			nativeOff, err := clock.Add(startNative, uint64(k)*e.D)
			if err != nil {
				return Representation{}, mediaerr.New(mediaerr.CatTimebase, "tick_overflow", err.Error()).
					With(xr.ID, 0, false)
			}
			off, err := sc.ToMaster(nativeOff)
			if err != nil {
				return Representation{}, mediaerr.New(mediaerr.CatTimebase, "tick_overflow", err.Error()).
					With(xr.ID, 0, false)
			}
			if e.D == 0 {
				return Representation{}, mediaerr.New(mediaerr.CatManifest, "zero_duration",
					"segment duration must be positive").With(xr.ID, 0, false)
			}
			dur, err := sc.ToMaster(e.D)
			if err != nil {
				return Representation{}, mediaerr.New(mediaerr.CatTimebase, "tick_overflow", err.Error()).
					With(xr.ID, 0, false)
			}
			url := expandTemplate(tpl.Media, xr.ID, idx)
			if _, dup := seenURL[url]; dup {
				return Representation{}, mediaerr.New(mediaerr.CatManifest, "duplicate_segment",
					"duplicate segment reference "+url).
					With(xr.ID, off, false).Wrap(mediaerr.ErrMalformed)
			}
			seenURL[url] = idx
			if idx == 0 {
				rep.StartTick = off
			}
			rep.Segments = append(rep.Segments, SegmentRef{
				Index: idx, URL: url, StartTick: off, DurationTick: dur, Timescale: tpl.Timescale,
			})
			rep.EndTick, _ = clock.Add(off, dur)
			idx++
		}
		cursor, err = clock.Add(startNative, uint64(e.R+1)*e.D)
		if err != nil {
			return Representation{}, mediaerr.New(mediaerr.CatTimebase, "tick_overflow", err.Error()).
				With(xr.ID, 0, false)
		}
	}
	if len(rep.Segments) == 0 {
		return Representation{}, mediaerr.New(mediaerr.CatManifest, "no_segments",
			"representation has no segments").With(xr.ID, 0, false)
	}
	return rep, nil
}

func expandTemplate(tmpl, repID string, idx uint32) string {
	out := strings.ReplaceAll(tmpl, "$RepresentationID$", repID)
	out = strings.ReplaceAll(out, "$Number$", strconv.FormatUint(uint64(idx), 10))
	return out
}

func codecAllowed(kind, codecs string) bool {
	c := strings.ToLower(codecs)
	switch kind {
	case "video":
		return strings.HasPrefix(c, "avc1") || strings.HasPrefix(c, "avc3") || c == "h264"
	case "audio":
		return strings.HasPrefix(c, "mp4a") || c == "aac"
	}
	return false
}

// parseISODuration accepts the subset PnDTnnHnnMnnS with integer seconds
// (fractional seconds are rounded to the master clock later if ever needed).
func parseISODuration(s string) (uint64, error) {
	if s == "" {
		return 0, mediaerr.New(mediaerr.CatManifest, "duration", "missing mediaPresentationDuration")
	}
	if !strings.HasPrefix(s, "P") {
		return 0, mediaerr.New(mediaerr.CatManifest, "duration", "duration must start with P")
	}
	rest := strings.TrimPrefix(s, "P")
	var totalSec uint64
	num := uint64(0)
	haveNum := false
	seenT := false
	for _, ch := range rest {
		switch {
		case ch >= '0' && ch <= '9':
			num = num*10 + uint64(ch-'0')
			haveNum = true
		case ch == 'T':
			seenT = true
		case ch == 'D':
			totalSec += num * 86400
			num, haveNum = 0, false
		case ch == 'H' && seenT:
			totalSec += num * 3600
			num, haveNum = 0, false
		case ch == 'M' && seenT:
			totalSec += num * 60
			num, haveNum = 0, false
		case ch == 'S' && seenT:
			totalSec += num
			num, haveNum = 0, false
		default:
			return 0, mediaerr.New(mediaerr.CatManifest, "duration", "unsupported duration token: "+s)
		}
	}
	if !haveNum && totalSec == 0 {
		return 0, mediaerr.New(mediaerr.CatManifest, "duration", "zero or empty duration")
	}
	d, err := clock.FromMillis(totalSec * 1000)
	if err != nil {
		return 0, mediaerr.New(mediaerr.CatTimebase, "tick_overflow", err.Error())
	}
	return d, nil
}
