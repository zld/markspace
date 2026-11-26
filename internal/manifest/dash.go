package manifest

import (
	"encoding/xml"
	"fmt"
	"math"
	"strconv"
	"strings"

	"continuity/internal/media"
)

type dashMPD struct {
	XMLName xml.Name     `xml:"MPD"`
	Type    string       `xml:"type,attr"`
	Periods []dashPeriod `xml:"Period"`
}
type dashPeriod struct {
	ID             string              `xml:"id,attr"`
	AdaptationSets []dashAdaptationSet `xml:"AdaptationSet"`
}
type dashAdaptationSet struct {
	ContentType     string               `xml:"contentType,attr"`
	MimeType        string               `xml:"mimeType,attr"`
	Representations []dashRepresentation `xml:"Representation"`
}
type dashRepresentation struct {
	ID              string               `xml:"id,attr"`
	Bandwidth       int64                `xml:"bandwidth,attr"`
	MimeType        string               `xml:"mimeType,attr"`
	SegmentTemplate *dashSegmentTemplate `xml:"SegmentTemplate"`
}
type dashSegmentTemplate struct {
	Timescale      uint32        `xml:"timescale,attr"`
	Media          string        `xml:"media,attr"`
	Initialization string        `xml:"initialization,attr"`
	StartNumber    int64         `xml:"startNumber,attr"`
	Timeline       *dashTimeline `xml:"SegmentTimeline"`
}
type dashTimeline struct {
	S []dashS `xml:"S"`
}
type dashS struct {
	T *int64 `xml:"t,attr"`
	D uint64 `xml:"d,attr"`
	R *int64 `xml:"r,attr"`
}

// ParseDASH parses a static MPD with Period/AdaptationSet/Representation and
// SegmentTemplate plus SegmentTimeline.
func ParseDASH(path, root string) (*Bundle, error) {
	return parseDASH(path, root, refTracker{}, DefaultLimits())
}

func parseDASH(path, root string, refs refTracker, limits Limits) (*Bundle, error) {
	data, err := readLimited(path, limits)
	if err != nil {
		return nil, err
	}
	var mpd dashMPD
	if err := xml.Unmarshal(data, &mpd); err != nil {
		line := 0
		if xe, ok := err.(*xml.SyntaxError); ok {
			line = xe.Line
		}
		return nil, &media.Fault{Phase: media.PhaseManifest, Code: media.CodeInvalidManifest,
			Source: path, Line: line, Err: err}
	}
	if mpd.Type != "" && !strings.EqualFold(mpd.Type, "static") {
		return nil, &media.Fault{Phase: media.PhaseManifest, Code: media.CodeInvalidManifest,
			Source: path, Detail: "only static MPD is supported"}
	}
	bundle := &Bundle{Entry: path, Root: root}
	for pi, period := range mpd.Periods {
		for _, as := range period.AdaptationSets {
			kind := strings.ToLower(as.ContentType)
			if kind == "" {
				kind = kindFromMime(as.MimeType)
			}
			for _, rep := range as.Representations {
				m, err := representationManifest(path, root, pi, kind, rep, data, refs, limits)
				if err != nil {
					return nil, err
				}
				bundle.Manifests = append(bundle.Manifests, m)
				bundle.Segments = append(bundle.Segments, m.Segments...)
			}
		}
	}
	if len(bundle.Segments) == 0 {
		return nil, &media.Fault{Phase: media.PhaseManifest, Code: media.CodeInvalidManifest, Source: path, Detail: "MPD has no segments"}
	}
	return bundle, nil
}

func representationManifest(path, root string, period int, kind string, rep dashRepresentation, data []byte, refs refTracker, limits Limits) (*Manifest, error) {
	tpl := rep.SegmentTemplate
	if tpl == nil || tpl.Timeline == nil || len(tpl.Timeline.S) == 0 {
		return nil, &media.Fault{Phase: media.PhaseManifest, Code: media.CodeInvalidManifest, Source: path, Detail: "Representation missing SegmentTimeline"}
	}
	timescale := tpl.Timescale
	if timescale == 0 {
		timescale = 1000
	}
	startNumber := tpl.StartNumber
	if startNumber == 0 {
		startNumber = 1
	}
	m := &Manifest{Format: "dash", Version: 1, Source: path, Root: root, Static: true, Timescale: timescale,
		MediaSequence: uint64(startNumber)}
	if kind == "" {
		kind = kindFromMime(rep.MimeType)
	}
	var timelineTime int64
	index := 0
	for _, s := range tpl.Timeline.S {
		if s.T != nil {
			timelineTime = *s.T
		}
		repeat := int64(0)
		if s.R != nil {
			if *s.R < 0 {
				return nil, dashOffsetFault(path, data, s.D, "open-ended SegmentTimeline repeat is not allowed")
			}
			repeat = *s.R
		}
		for j := int64(0); j <= repeat; j++ {
			if s.D > uint64(math.MaxInt64) {
				f := media.NewFault(media.PhaseManifest, media.CodeTimeOverflow,
					fmt.Sprintf("DASH duration %d overflows signed ticks", s.D))
				f.Source = path
				f.Offset = findAttrOffset(data, "d", strconv.FormatUint(s.D, 10))
				return nil, f
			}
			durNS, err := media.DurationFromTimescale(int64(s.D), timescale)
			if err != nil {
				f := media.WrapFault(media.PhaseManifest, media.CodeTimeOverflow, path, err)
				f.Offset = findAttrOffset(data, "d", strconv.FormatUint(s.D, 10))
				return nil, f
			}
			number := startNumber + int64(index)
			uri := dashTemplate(tpl.Media, rep.ID, number)
			abs, rel, err := SafeResolve(root, path, uri)
			if err != nil {
				return nil, err
			}
			if err := refs.check(abs, path, 0); err != nil {
				return nil, err
			}
			if index >= limits.MaxSegments {
				return nil, &media.Fault{Phase: media.PhaseManifest, Code: media.CodeSizeLimit, Source: path, Detail: "too many segments"}
			}
			startNS, err := media.DurationFromTimescale(timelineTime, timescale)
			if err != nil {
				return nil, err
			}
			seg := Segment{Index: index, Track: rep.ID, Kind: kind, Sequence: uint64(number), URI: uri,
				RelPath: rel, AbsPath: abs, StartNS: startNS, DurationNS: durNS, Source: path}
			if tpl.Initialization != "" {
				initURI := dashTemplate(tpl.Initialization, rep.ID, number)
				if ip, ir, err := SafeResolve(root, path, initURI); err == nil {
					seg.InitAbsPath, seg.InitRelPath = ip, ir
				} else {
					return nil, err
				}
			}
			m.Segments = append(m.Segments, seg)
			if timelineTime > math.MaxInt64-int64(s.D) {
				f := media.NewFault(media.PhaseManifest, media.CodeTimeOverflow, "SegmentTimeline time accumulator overflows")
				f.Source = path
				f.Offset = findAttrOffset(data, "d", strconv.FormatUint(s.D, 10))
				return nil, f
			}
			timelineTime += int64(s.D)
			index++
		}
	}
	_ = period
	return m, nil
}

func dashTemplate(pattern, repID string, number int64) string {
	out := strings.ReplaceAll(pattern, "$RepresentationID$", repID)
	if strings.Contains(out, "$Number") {
		// Supports $Number$ and $Number%0Nd$.
		if i := strings.Index(out, "$Number%"); i >= 0 {
			j := strings.Index(out[i:], "d$")
			if j > 0 {
				width, _ := strconv.Atoi(out[i+len("$Number%") : i+j])
				token := out[i : i+j+2]
				out = strings.Replace(out, token, fmt.Sprintf("%0*d", width, number), 1)
			}
		}
		out = strings.ReplaceAll(out, "$Number$", strconv.FormatInt(number, 10))
	}
	return out
}

func kindFromMime(mime string) string {
	mime = strings.ToLower(mime)
	switch {
	case strings.HasPrefix(mime, "audio") || strings.Contains(mime, "audio"):
		return "audio"
	case strings.HasPrefix(mime, "video") || strings.Contains(mime, "video"):
		return "video"
	default:
		return "video"
	}
}

func findAttrOffset(data []byte, key, value string) int64 {
	needle := key + `="` + value + `"`
	i := strings.Index(string(data), needle)
	if i < 0 {
		return 0
	}
	return int64(i)
}

func dashOffsetFault(path string, data []byte, d uint64, detail string) error {
	return &media.Fault{Phase: media.PhaseManifest, Code: media.CodeInvalidManifest, Source: path,
		Offset: findAttrOffset(data, "d", strconv.FormatUint(d, 10)), Detail: detail}
}
