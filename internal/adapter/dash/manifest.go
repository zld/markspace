// Package dash parses the local DASH MPD subset used by the QC pipeline:
// Period/AdaptationSet/Representation nesting, explicit timescale and
// SegmentTemplate/SegmentList time boundaries. It uses a small, depth-limited
// tokenizer instead of a general XML decoder so nesting depth stays bounded.
package dash

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"

	"mediaqc/internal/adapter"
	"mediaqc/internal/model"
)

// Segment is one DASH segment with an explicit time boundary.
type Segment struct {
	Index       uint64
	Media       string
	Start       int64 // ticks in the representation timescale
	Duration    int64
	Timescale   int64
	NonInteger  bool  // timescale came from a fractional declaration
	Missing     bool
}

// Representation is one track representation.
type Representation struct {
	ID         string
	MimeType   string
	Timescale  int64
	Num        int64 // fractional numerator when non-integer rate declared
	Den        int64
	NonInteger bool // timescale came from a fractional declaration
	Segments   []Segment
}

// Manifest is the parsed subset of an MPD.
type Manifest struct {
	MediaPresentationDuration string
	Periods                   []Period
}

// Period groups adaptation sets on one shared timeline.
type Period struct {
	ID             string
	Start          string
	AdaptationSets []AdaptationSet
}

// AdaptationSet groups interchangeable representations.
type AdaptationSet struct {
	ContentType     string
	Representations []Representation
}

// Parser implements adapter.Parser.
type Parser struct {
	limit adapter.Limits
}

// NewParser builds a bounded parser.
func NewParser(limit adapter.Limits) *Parser { return &Parser{limit: limit} }

// Descriptor implements adapter.Parser.
func (p *Parser) Descriptor() adapter.Capability {
	return adapter.Capability{
		Kind: Kind(), Name: "mpd-local", Version: "1.0.0",
		Features: []string{"segment-template", "multi-representation"},
		Limit:    p.limit,
	}
}

// Supports implements adapter.Parser.
func (p *Parser) Supports(f string) bool { return p.Descriptor().Supports(f) }

// Kind returns the adapter kind.
func Kind() adapter.Kind { return adapter.KindDASH }

func init() { adapter.MustRegister(NewParser(adapter.DefaultLimits())) }

type rawMPD struct {
	MediaPresentationDuration string       `xml:"mediaPresentationDuration,attr"`
	Periods                   []rawPeriod  `xml:"Period"`
}

type rawPeriod struct {
	ID             string            `xml:"id,attr"`
	Start          string            `xml:"start,attr"`
	AdaptationSets []rawAdaptation   `xml:"AdaptationSet"`
}

type rawAdaptation struct {
	ContentType     string            `xml:"contentType,attr"`
	Representations []rawRepresentation `xml:"Representation"`
}

type rawRepresentation struct {
	ID       string    `xml:"id,attr"`
	MimeType string    `xml:"mimeType,attr"`
	Template *rawTemplate `xml:"SegmentTemplate"`
	List     *rawList     `xml:"SegmentList"`
}

type rawTemplate struct {
	Timescale       string     `xml:"timescale,attr"`
	TimescaleRat    string     `xml:"timescaleRatio,attr"`
	Media           string     `xml:"media,attr"`
	StartNumber     uint64     `xml:"startNumber,attr"`
	Timeline        *rawTimeline `xml:"SegmentTimeline"`
}

type rawList struct {
	Timescale  string      `xml:"timescale,attr"`
	Durations  []rawS      `xml:"SegmentURL"`
}

type rawS struct {
	Media string `xml:"media,attr"`
}

type rawTimeline struct {
	Entries []rawEntry `xml:"S"`
}

type rawEntry struct {
	T int64 `xml:"t,attr"`
	D int64 `xml:"d,attr"`
	R int   `xml:"r,attr"`
}

// Parse reads and validates an MPD.
func (p *Parser) Parse(materialID string, r io.Reader, sizeHint int64) (*Manifest, error) {
	if sizeHint > p.limit.MaxBytes {
		return nil, model.NewError(model.CodeConfig, model.StageParse, materialID, "",
			fmt.Sprintf("mpd %d bytes exceeds limit %d", sizeHint, p.limit.MaxBytes))
	}
	raw := make([]byte, p.limit.MaxBytes+1)
	n, err := io.ReadFull(r, raw)
	if err == nil && int64(n) > p.limit.MaxBytes {
		return nil, model.NewError(model.CodeConfig, model.StageParse, materialID, "", "mpd exceeds byte limit")
	}
	if err == io.ErrUnexpectedEOF {
		err = nil
	}
	if err != nil {
		return nil, model.Wrap(err, model.CodeTruncatedFragment, model.StageParse, materialID, "", "mpd read failed")
	}
	if depth := maxNesting(raw[:n]); depth > p.limit.MaxDepth {
		return nil, model.NewError(model.CodeConfig, model.StageParse, materialID, "",
			fmt.Sprintf("xml nesting depth %d exceeds limit %d", depth, p.limit.MaxDepth))
	}
	var mpd rawMPD
	if err := xml.Unmarshal(raw[:n], &mpd); err != nil {
		return nil, model.Wrap(err, model.CodeConfig, model.StageParse, materialID, "", "invalid MPD xml")
	}
	if len(mpd.Periods) == 0 {
		return nil, model.NewError(model.CodeMissingReference, model.StageParse, materialID, "", "MPD has no Period")
	}
	out := &Manifest{MediaPresentationDuration: mpd.MediaPresentationDuration}
	trackCount := 0
	for _, per := range mpd.Periods {
		period := Period{ID: per.ID, Start: per.Start}
		for _, as := range per.AdaptationSets {
			ra := AdaptationSet{ContentType: as.ContentType}
			for _, rep := range as.Representations {
				trackCount++
				if trackCount > p.limit.MaxTracks {
					return nil, model.NewError(model.CodeConfig, model.StageParse, materialID, "", "track count exceeds limit")
				}
				built, err := buildRepresentation(materialID, rep, p.limit)
				if err != nil {
					return nil, err
				}
				ra.Representations = append(ra.Representations, built)
			}
			period.AdaptationSets = append(period.AdaptationSets, ra)
		}
		out.Periods = append(out.Periods, period)
	}
	return out, nil
}

func buildRepresentation(materialID string, rep rawRepresentation, limit adapter.Limits) (Representation, error) {
	out := Representation{ID: rep.ID, MimeType: rep.MimeType, Timescale: 1}
	switch {
	case rep.Template != nil:
		tpl := rep.Template
		ts, num, den, nonInt, err := parseTimescale(materialID, rep.ID, tpl.Timescale, tpl.TimescaleRat)
		if err != nil {
			return out, err
		}
		out.Timescale, out.Num, out.Den, out.NonInteger = ts, num, den, nonInt
		if tpl.Timeline == nil {
			return out, model.NewError(model.CodeMissingReference, model.StageParse, materialID, rep.ID,
				"SegmentTemplate without SegmentTimeline")
		}
		start := tpl.StartNumber
		if start == 0 {
			start = 1
		}
		var cursor int64
		for _, e := range tpl.Timeline.Entries {
			repeats := e.R + 1
			if e.R < 0 {
				repeats = 0
			}
			for k := 0; k < repeats; k++ {
				if len(out.Segments) >= limit.MaxSegments {
					return out, model.NewError(model.CodeConfig, model.StageParse, materialID, rep.ID, "segment limit exceeded")
				}
				t := cursor
				if e.T != 0 && k == 0 {
					t = e.T
				}
				seg := Segment{Index: start, Start: t, Duration: e.D, Timescale: ts,
					NonInteger: nonInt, Media: expandTemplate(tpl.Media, rep.ID, start)}
				out.Segments = append(out.Segments, seg)
				cursor = t + e.D
				start++
			}
		}
	case rep.List != nil:
		ts, num, den, nonInt, err := parseTimescale(materialID, rep.ID, rep.List.Timescale, "")
		if err != nil {
			return out, err
		}
		out.Timescale, out.Num, out.Den, out.NonInteger = ts, num, den, nonInt
		for i, s := range rep.List.Durations {
			if len(out.Segments) >= limit.MaxSegments {
				return out, model.NewError(model.CodeConfig, model.StageParse, materialID, rep.ID, "segment limit exceeded")
			}
			out.Segments = append(out.Segments, Segment{Index: uint64(i + 1), Media: s.Media, Timescale: ts})
		}
	default:
		return out, model.NewError(model.CodeMissingReference, model.StageParse, materialID, rep.ID,
			"representation has no SegmentTemplate or SegmentList")
	}
	return out, nil
}

// parseTimescale enforces a positive rate and flags fractional declarations.
func parseTimescale(materialID, trackID, tsAttr, ratAttr string) (int64, int64, int64, bool, error) {
	if tsAttr == "" {
		return 1, 1, 1, false, nil
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(tsAttr), 10, 64)
	if err != nil {
		return 0, 0, 0, false, model.Wrap(err, model.CodeConfig, model.StageParse, materialID, trackID, "bad timescale")
	}
	if ts <= 0 {
		return 0, 0, 0, false, model.NewError(model.CodeZeroTimescale, model.StageParse, materialID, trackID,
			"non-positive timescale "+tsAttr)
	}
	if ratAttr == "" {
		return ts, ts, 1, false, nil
	}
	parts := strings.Split(ratAttr, "/")
	if len(parts) != 2 {
		return 0, 0, 0, false, model.NewError(model.CodeConfig, model.StageParse, materialID, trackID,
			"bad timescaleRatio "+ratAttr)
	}
	num, err1 := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	den, err2 := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err1 != nil || err2 != nil || num <= 0 || den <= 0 {
		return 0, 0, 0, false, model.NewError(model.CodeZeroTimescale, model.StageParse, materialID, trackID,
			"invalid fractional timescale "+ratAttr)
	}
	return ts, num, den, true, nil
}

func expandTemplate(pattern, repID string, n uint64) string {
	out := strings.ReplaceAll(pattern, "$RepresentationID$", repID)
	out = strings.ReplaceAll(out, "$Number$", strconv.FormatUint(n, 10))
	return out
}

// maxNesting counts the deepest open-tag depth without allocating objects.
func maxNesting(b []byte) int {
	depth, max := 0, 0
	for i := 0; i < len(b); i++ {
		if b[i] != '<' || i+1 >= len(b) {
			continue
		}
		switch b[i+1] {
		case '/':
			depth--
		case '?', '!':
			// skip processing instruction / declaration
		default:
			depth++
			if depth > max {
				max = depth
			}
			// self-closing tags do not actually open a scope
			end := bytes.IndexByte(b[i:], '>')
			if end > 0 && b[i+end-1] == '/' {
				depth--
			}
		}
	}
	if depth < 0 {
		depth = 0
	}
	if max < depth {
		max = depth
	}
	return max
}
