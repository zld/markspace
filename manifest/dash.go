package manifest

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// lineDecoder wraps xml.Decoder so syntax errors can carry an input line.
type lineDecoder struct {
	dec  *xml.Decoder
	line int
}

type dashMPD struct {
	XMLName xml.Name    `xml:"MPD"`
	Periods []dashPeriod `xml:"Period"`
}

type dashPeriod struct {
	ID       string             `xml:"id,attr"`
	Adapt    []dashAdaptation   `xml:"AdaptationSet"`
}

type dashAdaptation struct {
	ContentType string           `xml:"contentType,attr"`
	MimeType    string           `xml:"mimeType,attr"`
	Template    dashTemplate     `xml:"SegmentTemplate"`
}

type dashTemplate struct {
	Timescale      uint64          `xml:"timescale,attr"`
	Initialization  string          `xml:"initialization,attr"`
	Media           string          `xml:"media,attr"`
	StartNumber     int64           `xml:"startNumber,attr"`
	Timeline        dashSegTimeline `xml:"SegmentTimeline"`
}

type dashSegTimeline struct {
	S []dashS `xml:"S"`
}

type dashS struct {
	T int64 `xml:"t,attr"`
	D int64 `xml:"d,attr"`
	R int64 `xml:"r,attr"`
}

// ParseDASH parses a DASH MPD limited to SegmentTemplate/SegmentTimeline.
func ParseDASH(r io.Reader, programKey, baseDir string) (*Manifest, error) {
	asset := programKey + "#dash"
	buf, err := io.ReadAll(io.LimitReader(r, 64*1024*1024))
	if err != nil {
		return nil, &Error{Code: ErrSyntax, Asset: asset, Stage: "dash.read", Message: err.Error()}
	}
	dec := &lineDecoder{dec: xml.NewDecoder(bytes.NewReader(buf))}
	var root dashMPD
	for {
		tok, derr := dec.dec.Token()
		if derr == io.EOF {
			break
		}
		if derr != nil {
			return nil, &Error{Code: ErrSyntax, Asset: asset, Stage: "dash.xml",
				Line: dec.line, Message: derr.Error()}
		}
		dec.line = lineAt(buf, dec.dec.InputOffset())
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "MPD" {
			if perr := dec.dec.DecodeElement(&root, &se); perr != nil {
				return nil, &Error{Code: ErrSyntax, Asset: asset, Stage: "dash.decode",
					Line: dec.line, Message: perr.Error()}
			}
			break
		}
	}
	if len(root.Periods) == 0 {
		return nil, &Error{Code: ErrSyntax, Asset: asset, Stage: "dash.period", Message: "no Period element"}
	}
	m := &Manifest{ProgramKey: programKey, Format: "dash", BaseDir: baseDir}
	var totalNS int64
	for pi, per := range root.Periods {
		for ai, ad := range per.Adapt {
			kind, perr := dashKind(ad)
			if perr != nil {
				return nil, &Error{Code: ErrUnsupported, Asset: asset, Stage: "dash.adaptation",
					Line: dec.line, Message: perr.Error()}
			}
			trackID := fmt.Sprintf("%s%d", kind, ai)
			track := m.findTrack(trackID, true)
			track.Kind = kind
			tpl := ad.Template
			if tpl.Timescale == 0 {
				return nil, &Error{Code: ErrZeroTimescale, Asset: asset, Track: trackID,
					Stage: "dash.timescale", Message: "SegmentTemplate timescale missing or zero"}
			}
			track.Timescale = tpl.Timescale
			if tpl.Initialization != "" {
				if !SafeURI(dashSubst(tpl.Initialization, 0, 0)) {
					return nil, &Error{Code: ErrPathTraversal, Asset: asset, Track: trackID,
						Stage: "dash.initialization", Message: "illegal init template"}
				}
				track.InitURI = dashSubst(tpl.Initialization, 0, 0)
			}
			if tpl.Media == "" {
				return nil, &Error{Code: ErrSyntax, Asset: asset, Track: trackID,
					Stage: "dash.template", Message: "media template missing"}
			}
			startNo := tpl.StartNumber
			if startNo == 0 {
				startNo = 1
			}
			var cursor int64 // timescale units
			seq := startNo
			for _, s := range tpl.Timeline.S {
				if s.D <= 0 {
					return nil, &Error{Code: ErrBadDuration, Asset: asset, Track: trackID,
						Stage: "dash.timeline", Message: "S@d must be positive: " + strconv.FormatInt(s.D, 10)}
				}
				if s.T != 0 {
					cursor = s.T
				}
				repeats := s.R
				if repeats < 0 {
					return nil, &Error{Code: ErrBadDuration, Asset: asset, Track: trackID,
						Stage: "dash.timeline", Message: "negative S@r repeat"}
				}
				for k := int64(0); k <= repeats; k++ {
					startNS, ok1 := scaleChecked(cursor, tpl.Timescale)
					durNS, ok2 := scaleChecked(s.D, tpl.Timescale)
					if !ok1 || !ok2 {
						return nil, &Error{Code: ErrOverflow, Asset: asset, Track: trackID,
							Stage: "dash.scale", Message: "timescale->ns overflow"}
					}
					if err := checkNSRange(startNS, durNS, asset, trackID, "dash.timeline", dec.line); err != nil {
						return nil, err
					}
					uri := dashSubst(tpl.Media, seq, cursor)
					if !SafeURI(uri) {
						return nil, &Error{Code: ErrPathTraversal, Asset: asset, Track: trackID,
							Stage: "dash.media", Message: "illegal media uri: " + uri}
					}
					track.Segments = append(track.Segments, SegmentRef{
						Index:      len(track.Segments),
						URI:        uri,
						StartNS:    startNS,
						DurationNS: durNS,
						Sequence:   seq,
					})
					if cursor > math.MaxInt64-s.D {
						return nil, &Error{Code: ErrOverflow, Asset: asset, Track: trackID,
							Stage: "dash.timeline", Message: "timeline cursor overflow"}
					}
					cursor += s.D
					seq++
				}
			}
			if len(track.Segments) == 0 {
				return nil, &Error{Code: ErrBadDuration, Asset: asset, Track: trackID,
					Stage: "dash.timeline", Message: fmt.Sprintf("period %d adaptation %d has no segments", pi, ai)}
			}
			if end := track.Segments[len(track.Segments)-1].StartNS +
				track.Segments[len(track.Segments)-1].DurationNS; end > totalNS {
				totalNS = end
			}
		}
	}
	if len(m.Tracks) == 0 {
		return nil, &Error{Code: ErrSyntax, Asset: asset, Stage: "dash.adaptation", Message: "no usable AdaptationSet"}
	}
	m.DurationNS = totalNS
	return m, nil
}

func dashKind(a dashAdaptation) (string, error) {
	ct := a.ContentType
	if ct == "" {
		ct = strings.TrimPrefix(strings.SplitN(a.MimeType, "/", 2)[0], "")
		if i := strings.IndexByte(a.MimeType, '/'); i > 0 {
			ct = a.MimeType[:i]
		}
	}
	switch ct {
	case KindVideo:
		return KindVideo, nil
	case KindAudio:
		return KindAudio, nil
	default:
		return "", fmt.Errorf("unsupported content type: %q", ct)
	}
}

func dashSubst(tmpl string, number, time int64) string {
	out := tmpl
	out = strings.ReplaceAll(out, "$Number$", strconv.FormatInt(number, 10))
	out = strings.ReplaceAll(out, "$Time$", strconv.FormatInt(time, 10))
	return out
}

// scaleChecked converts timescale units to nanoseconds with overflow guard.
func scaleChecked(units int64, timescale uint64) (int64, bool) {
	if units < 0 || timescale == 0 {
		return 0, false
	}
	u := uint64(units)
	const maxUnits = math.MaxInt64 / 1_000_000_000
	if u > maxUnits {
		return 0, false
	}
	ns := (u * 1_000_000_000) / timescale
	return int64(ns), ns <= math.MaxInt64
}

func lineAt(buf []byte, offset int64) int {
	if offset < 0 || offset > int64(len(buf)) {
		offset = int64(len(buf))
	}
	return bytes.Count(buf[:offset], []byte{'\n'}) + 1
}
