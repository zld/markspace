// Package manifest parses DASH MPDs and HLS play lists into the normalized
// model.Manifest on the unified nanosecond playout timeline.
package manifest

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"mediad/internal/model"
)

// --- ISO 8601 duration (PnDTnHnMnS subset, decimals allowed) ---

// ParseISODuration parses durations such as PT10S and PT2.500S into
// nanoseconds. An empty string is reported as a missing-duration error.
func ParseISODuration(s string) (model.PlayTime, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if s[0] != 'P' {
		return 0, fmt.Errorf("duration %q must start with P", s)
	}
	var ns int64
	num := ""
	inTime := false
	flushed := false
	flush := func(unit byte) error {
		if num == "" {
			return fmt.Errorf("duration %q missing number before %c", s, unit)
		}
		whole, frac := num, ""
		if i := strings.IndexByte(num, '.'); i >= 0 {
			whole, frac = num[:i], num[i+1:]
		}
		v, err := strconv.ParseInt(whole, 10, 64)
		if err != nil {
			return fmt.Errorf("duration %q: %w", s, err)
		}
		mult := map[byte]int64{
			'H': 3600, 'M': 60, 'S': 1,
		}
		secs, ok := mult[unit]
		if !ok {
			return fmt.Errorf("duration %q: unsupported unit %c", s, unit)
		}
		ns += v * secs * 1_000_000_000
		if frac != "" {
			f, err := strconv.ParseInt(frac, 10, 64)
			if err != nil {
				return fmt.Errorf("duration %q: bad fraction", s)
			}
			pow := int64(1)
			for i := 0; i < len(frac); i++ {
				pow *= 10
			}
			ns += f * (secs * 1_000_000_000 / pow)
		}
		num = ""
		return nil
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c == 'T':
			inTime = true
		case c >= '0' && c <= '9' || c == '.':
			num += string(c)
		case inTime && (c == 'H' || c == 'M' || c == 'S'):
			if err := flush(c); err != nil {
				return 0, err
			}
			flushed = true
		default:
			return 0, fmt.Errorf("duration %q: unexpected %c", s, c)
		}
	}
	if num != "" {
		return 0, fmt.Errorf("duration %q: trailing number without unit", s)
	}
	if !flushed {
		return 0, fmt.Errorf("duration %q contains no time unit", s)
	}
	return model.PlayTime(ns), nil
}

// --- MPD XML shape ---

type xmlMPD struct {
	XMLName            xml.Name      `xml:"MPD"`
	Duration           string        `xml:"mediaPresentationDuration,attr"`
	Periods            []xmlPeriod   `xml:"Period"`
}

type xmlPeriod struct {
	Sets []xmlAdaptationSet `xml:"AdaptationSet"`
}

type xmlAdaptationSet struct {
	ContentType     string             `xml:"contentType,attr"`
	MimeType        string             `xml:"mimeType,attr"`
	Representations []xmlRepresentation `xml:"Representation"`
}

type xmlRepresentation struct {
	ID       string         `xml:"id,attr"`
	Template xmlSegTemplate `xml:"SegmentTemplate"`
}

type xmlSegTemplate struct {
	TimeScale      uint32           `xml:"timescale,attr"`
	Init           string           `xml:"initialization,attr"`
	Media          string           `xml:"media,attr"`
	StartNumber    int64            `xml:"startNumber,attr"`
	Timeline       []xmlTimelineEntry `xml:"SegmentTimeline>S"`
}

type xmlTimelineEntry struct {
	T int64 `xml:"t,attr"`
	D int64 `xml:"d,attr"`
	R int64 `xml:"r,attr"`
}

func loc(mpd, rep string, entry int) model.SourceLoc {
	return model.SourceLoc{Manifest: mpd, Representation: rep, Entry: entry, Frame: -1, Offset: -1}
}

// ParseDASH parses MPD bytes into a normalized manifest. All validation
// failures are *model.SourceError with representation and entry positions.
func ParseDASH(data []byte, path string) (*model.Manifest, error) {
	var x xmlMPD
	if err := xml.Unmarshal(data, &x); err != nil {
		return nil, model.NewError(model.KindCorruptBox, loc(path, "", -1), "invalid MPD XML: "+err.Error())
	}
	duration, err := ParseISODuration(x.Duration)
	if err != nil {
		return nil, model.NewError(model.KindMissingDuration, loc(path, "", -1), err.Error())
	}
	if len(x.Periods) == 0 {
		return nil, model.NewError(model.KindMissingDuration, loc(path, "", -1), "MPD has no Period")
	}
	m := &model.Manifest{
		Kind: "dash", Path: path, TimeScale: 1000, Duration: duration,
	}
	for pIdx, p := range x.Periods {
		for _, set := range p.Sets {
			kind := kindFromContent(set.ContentType, set.MimeType)
			for _, xr := range set.Representations {
				rl := loc(path, xr.ID, -1)
				ts := model.TimeScale(xr.Template.TimeScale)
				if !ts.Valid() {
					return nil, model.NewError(model.KindZeroTimescale, rl,
						fmt.Sprintf("representation %q declares no usable timescale", xr.ID))
				}
				if xr.Template.StartNumber < 0 {
					return nil, model.NewError(model.KindIndexOutOfRange, rl,
						fmt.Sprintf("negative startNumber %d", xr.Template.StartNumber))
				}
				segs, err := buildSegments(xr, ts, duration, path)
				if err != nil {
					return nil, err
				}
				m.Representations = append(m.Representations, model.Representation{
					ID: xr.ID, Kind: kind, TimeScale: ts, Segments: segs,
				})
			}
		}
		_ = pIdx
	}
	if len(m.Representations) == 0 {
		return nil, model.NewError(model.KindMissingDuration, loc(path, "", -1), "MPD has no representations")
	}
	return m, nil
}

func buildSegments(xr xmlRepresentation, ts model.TimeScale, max model.PlayTime, path string) ([]model.SegmentRef, error) {
	tpl := xr.Template
	if len(tpl.Timeline) == 0 {
		return nil, model.NewError(model.KindMissingDuration, loc(path, xr.ID, -1),
			"SegmentTemplate has no SegmentTimeline")
	}
	var out []model.SegmentRef
	var tick int64
	entry := 0
	for _, s := range tpl.Timeline {
		if s.T != 0 && entry == 0 {
			tick = s.T
		}
		if s.D <= 0 {
			return nil, model.NewError(model.KindFieldOutOfRange, loc(path, xr.ID, entry),
				fmt.Sprintf("timeline entry duration %d out of range", s.D))
		}
		if s.R < 0 {
			return nil, model.NewError(model.KindIndexOutOfRange, loc(path, xr.ID, entry),
				fmt.Sprintf("negative repeat count %d", s.R))
		}
		for k := int64(0); k <= s.R; k++ {
			startNS, err := model.ScaleRound(tick, ts)
			if err != nil {
				return nil, model.WrapError(model.KindFieldOutOfRange, loc(path, xr.ID, entry), err)
			}
			durNS, err := model.ScaleRound(s.D, ts)
			if err != nil {
				return nil, model.WrapError(model.KindFieldOutOfRange, loc(path, xr.ID, entry), err)
			}
			end, err := model.Add(startNS, durNS)
			if err != nil || end > max {
				return nil, model.NewError(model.KindIndexOutOfRange, loc(path, xr.ID, entry),
					fmt.Sprintf("segment end %d exceeds presentation duration %d", end, max))
			}
			num := tpl.StartNumber + int64(entry)
			url := expandTemplate(tpl.Media, num, xr.ID)
			out = append(out, model.SegmentRef{
				Index: entry, URL: url,
				StartTicks: tick, DurationTicks: s.D,
				Start: startNS, Duration: durNS,
			})
			sum, ok := addChecked(tick, s.D)
			if !ok {
				return nil, model.NewError(model.KindFieldOutOfRange, loc(path, xr.ID, entry),
					"timeline tick accumulation overflow")
			}
			tick = sum
			entry++
		}
	}
	return out, nil
}

func addChecked(a, b int64) (int64, bool) {
	s := a + b
	if (b > 0 && s < a) || (b < 0 && s > a) {
		return 0, false
	}
	return s, true
}

func expandTemplate(pattern string, number int64, repID string) string {
	r := strings.NewReplacer("$Number$", strconv.FormatInt(number, 10),
		"$RepresentationID$", repID)
	return r.Replace(pattern)
}

func kindFromContent(content, mime string) model.TrackKind {
	c := content
	if c == "" {
		c = mime
	}
	if strings.HasPrefix(c, "audio") {
		return model.TrackAudio
	}
	return model.TrackVideo
}

// ParseDASHFile reads and parses an MPD; a missing file is a sourced error.
func ParseDASHFile(path string) (*model.Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, model.NewError(model.KindManifestMissing, loc(path, "", -1), err.Error())
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, model.WrapError(model.KindCorruptBox, loc(path, "", -1), err)
	}
	return ParseDASH(data, path)
}
