package manifest

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"continuity/internal/clock"
)

// dashXML mirrors the subset of MPD schema the kernel accepts.
type dashXML struct {
	XMLName        xml.Name      `xml:"MPD"`
	Duration       string        `xml:"mediaPresentationDuration,attr"`
	Periods        []dashPeriod  `xml:"Period"`
}

type dashPeriod struct {
	AdaptationSets []dashAdaptSet `xml:"AdaptationSet"`
}

type dashAdaptSet struct {
	SegmentTemplate *dashSegTemplate `xml:"SegmentTemplate"`
	Representations []dashRepr       `xml:"Representation"`
}

type dashSegTemplate struct {
	Timescale      int64          `xml:"timescale,attr"`
	Initialization string         `xml:"initialization,attr"`
	Media          string         `xml:"media,attr"`
	StartNumber    int64          `xml:"startNumber,attr"`
	Timeline       *dashTimeline  `xml:"SegmentTimeline"`
}

type dashTimeline struct {
	Items []dashS `xml:"S"`
}

type dashS struct {
	T         int64 `xml:"t,attr"`
	D         int64 `xml:"d,attr"`
	R         int64 `xml:"r,attr"`
	rawOffset int64
}

type dashRepr struct {
	ID        string `xml:"id,attr"`
	Bandwidth int64  `xml:"bandwidth,attr"`
}

// lineIndex maps byte offsets to 1-based line numbers for raw MPD content.
type lineIndex struct {
	raw   []byte
	starts []int // byte offset of the start of each line
}

func newLineIndex(raw []byte) *lineIndex {
	li := &lineIndex{starts: []int{0}}
	for i, b := range raw {
		if b == '\n' {
			li.starts = append(li.starts, i+1)
		}
	}
	li.raw = raw
	return li
}

func (li *lineIndex) lineAt(off int64) int {
	// binary search: greatest start <= off.
	lo, hi := 0, len(li.starts)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if int64(li.starts[mid]) <= off {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo + 1
}

// ParseDASH reads and parses an MPD from a sanitized local absolute path.
func ParseDASH(path string) (*Manifest, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, clock.New(clock.KindIO, "parse dash", "abs path: %v", err)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, clock.AtFile(clock.New(clock.KindIO, "parse dash",
			"read manifest: %v", err), abs)
	}
	if int64(len(raw)) > MaxManifestBytes {
		return nil, clock.AtFile(clock.New(clock.KindInvalidInput, "parse dash",
			"manifest exceeds %d bytes", MaxManifestBytes), abs)
	}
	li := newLineIndex(raw)

	// Pre-pass: capture the byte offset of every <S> start element in
	// document order so decoded timeline items retain line locations.
	sOffsets, offErr := sElementOffsets(bytes.NewReader(raw))
	if offErr != nil {
		return nil, clock.AtFile(offErr, abs)
	}

	dec := xml.NewDecoder(bytes.NewReader(raw))
	var doc dashXML
	if err := dec.Decode(&doc); err != nil {
		var se *xml.SyntaxError
		line := 0
		if asSyntaxError(err, &se) {
			line = se.Line
		}
		return nil, clock.AtFile(clock.AtLine(
			clock.New(clock.KindInvalidInput, "parse dash",
				"invalid XML: %v", err), line), abs)
	}
	assignSOffsets(&doc, sOffsets)
	if len(doc.Periods) == 0 {
		return nil, clock.AtFile(clock.New(clock.KindInvalidInput, "parse dash",
			"MPD contains no Period"), abs)
	}

	m := &Manifest{Kind: KindDASH, Path: abs, Dir: filepath.Dir(abs)}
	if doc.Duration != "" {
		dur, derr := parseISODuration(doc.Duration)
		if derr != nil {
			return nil, clock.AtFile(derr, abs)
		}
		m.MediaPresentSec = dur
	}

	for pi, period := range doc.Periods {
		for ai, as := range period.AdaptationSets {
			if as.SegmentTemplate == nil {
				return nil, clock.AtFile(clock.New(clock.KindInvalidInput,
					"parse dash", "Period[%d] AdaptationSet[%d] has no SegmentTemplate",
					pi, ai), abs)
			}
			tpl := as.SegmentTemplate
			if tpl.Media == "" {
				return nil, clock.AtFile(clock.New(clock.KindInvalidInput,
					"parse dash", "SegmentTemplate missing media attribute"), abs)
			}
			if tpl.Timeline == nil || len(tpl.Timeline.Items) == 0 {
				return nil, clock.AtFile(clock.New(clock.KindInvalidInput,
					"parse dash", "only SegmentTimeline templates are supported"), abs)
			}
			ts := tpl.Timescale
			if ts <= 0 {
				ts = 1
			}
			for _, rx := range as.Representations {
				rep, perr := buildDASHRepr(abs, tpl, ts, rx, li)
				if perr != nil {
					return nil, perr
				}
				m.Representations = append(m.Representations, rep)
			}
		}
	}
	if len(m.Representations) == 0 {
		return nil, clock.AtFile(clock.New(clock.KindInvalidInput, "parse dash",
			"no representations found"), abs)
	}
	if err := CheckSegmentFiles(m); err != nil {
		return nil, err
	}
	return m, nil
}

func buildDASHRepr(file string, tpl *dashSegTemplate, timescale int64,
	rx dashRepr, li *lineIndex) (Representation, error) {
	rep := Representation{ID: rx.ID, BandwidthBPS: rx.Bandwidth}
	seenSeq := make(map[int64]int)
	seenURL := make(map[string]int)
	var tCursor int64
	num := tpl.StartNumber
	if num == 0 {
		num = 1
	}
	idx := 0
	for itemPos, item := range tpl.Timeline.Items {
		if item.D <= 0 {
			line := li.lineAt(int64(item.rawOffset))
			return rep, clock.AtFile(clock.AtLine(clock.New(clock.KindInvalidInput,
				"parse dash", "S[%d] has non-positive d=%d", itemPos, item.D), line), file)
		}
		if item.T != 0 || itemPos == 0 {
			if item.T != 0 {
				tCursor = item.T
			}
		}
		repeats := item.R
		if repeats < 0 {
			return rep, clock.AtFile(clock.New(clock.KindInvalidInput, "parse dash",
				"S[%d] negative r not supported", itemPos), file)
		}
		for k := int64(0); k <= repeats; k++ {
			url := expandTemplate(tpl.Media, rx.ID, num, tCursor)
			line := li.lineAt(int64(item.rawOffset))
			if prev, dup := seenSeq[num]; dup {
				return rep, clock.AtFile(clock.AtLine(clock.New(clock.KindInvalidInput,
					"parse dash", "duplicate segment number %d (also at segment index %d)",
					num, prev), line), file)
			}
			if prev, dup := seenURL[url]; dup {
				return rep, clock.AtFile(clock.AtLine(clock.New(clock.KindInvalidInput,
					"parse dash", "duplicate segment reference %q (also at index %d)",
					url, prev), line), file)
			}
			seenSeq[num] = idx
			seenURL[url] = idx
			start, err := clock.R(tCursor, timescale)
			if err != nil {
				return rep, clock.AtFile(clock.AtLine(err, line), file)
			}
			dur, err := clock.R(item.D, timescale)
			if err != nil {
				return rep, clock.AtFile(clock.AtLine(err, line), file)
			}
			rep.Segments = append(rep.Segments, Segment{
				Index:    idx,
				Sequence: int(num),
				URL:      url,
				Start:    start,
				Duration: dur,
				Line:     line,
				Offset:   item.rawOffset,
			})
			tCursor += item.D
			num++
			idx++
		}
	}
	return rep, nil
}

func expandTemplate(tpl, repID string, number, time int64) string {
	out := tpl
	out = strings.ReplaceAll(out, "$$", "\x00")
	out = strings.ReplaceAll(out, "$RepresentationID$", repID)
	out = strings.ReplaceAll(out, "$Number$", strconv.FormatInt(number, 10))
	out = strings.ReplaceAll(out, "$Time$", strconv.FormatInt(time, 10))
	out = strings.ReplaceAll(out, "\x00", "$")
	return out
}

func asSyntaxError(err error, se **xml.SyntaxError) bool {
	if e, ok := err.(*xml.SyntaxError); ok {
		*se = e
		return true
	}
	return false
}

// sElementOffsets returns raw byte offsets of every SegmentTimeline <S>
// start element, in document order across the whole MPD.
func sElementOffsets(r *bytes.Reader) ([]int64, error) {
	dec := xml.NewDecoder(r)
	var out []int64
	for {
		tok, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				return out, nil
			}
			return nil, clock.New(clock.KindInvalidInput, "parse dash",
				"tokenize MPD: %v", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local == "S" {
			out = append(out, dec.InputOffset())
		}
	}
}

func assignSOffsets(doc *dashXML, offsets []int64) {
	i := 0
	for _, p := range doc.Periods {
		for _, as := range p.AdaptationSets {
			if as.SegmentTemplate == nil || as.SegmentTemplate.Timeline == nil {
				continue
			}
			for k := range as.SegmentTemplate.Timeline.Items {
				if i < len(offsets) {
					as.SegmentTemplate.Timeline.Items[k].rawOffset = offsets[i]
					i++
				}
			}
		}
	}
}

// parseISODuration parses the restricted xs:duration form used by MPD
// mediaPresentationDuration: P[nDT...]H M S, e.g. PT8.0S, P1DT12H.
func parseISODuration(s string) (clock.Rat, error) {
	orig := s
	if len(s) == 0 || s[0] != 'P' {
		return clock.Rat{}, clock.New(clock.KindInvalidInput, "parse dash",
			"bad ISO-8601 duration %q", orig)
	}
	s = s[1:]
	var totalSec int64
	frac := clock.FromInt(0)
	num := int64(0)
	haveNum := false
	seenT := false
	flush := func(unit byte) error {
		switch unit {
		case 'H':
			v, err := mul64safe(num, 3600)
			if err != nil {
				return err
			}
			totalSec += v
		case 'M':
			if !seenT {
				return clock.New(clock.KindInvalidInput, "parse dash",
					"months not allowed in duration %q", orig)
			}
			v, err := mul64safe(num, 60)
			if err != nil {
				return err
			}
			totalSec += v
		case 'S':
			totalSec += num
		case 'D':
			v, err := mul64safe(num, 86400)
			if err != nil {
				return err
			}
			totalSec += v
		default:
			return clock.New(clock.KindInvalidInput, "parse dash",
				"bad duration unit %q in %q", string(unit), orig)
		}
		return nil
	}
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == 'T':
			seenT = true
			i++
		case c >= '0' && c <= '9':
			num = 0
			haveNum = true
			for i < len(s) && s[i] >= '0' && s[i] <= '9' {
				num = num*10 + int64(s[i]-'0')
				i++
			}
		case c == '.':
			i++
			digits, scale := int64(0), int64(1)
			for i < len(s) && s[i] >= '0' && s[i] <= '9' {
				digits = digits*10 + int64(s[i]-'0')
				scale *= 10
				i++
			}
			if i >= len(s) || s[i] != 'S' {
				return clock.Rat{}, clock.New(clock.KindInvalidInput,
					"parse dash", "fractional component only allowed on seconds in %q", orig)
			}
			f, err := clock.R(digits, scale)
			if err != nil {
				return clock.Rat{}, err
			}
			frac = f
			totalSec += num
			num, haveNum = 0, false
			i++
		case c == 'H' || c == 'M' || c == 'S' || c == 'D':
			if !haveNum {
				return clock.Rat{}, clock.New(clock.KindInvalidInput, "parse dash",
					"unit %q without number in %q", string(c), orig)
			}
			if err := flush(c); err != nil {
				return clock.Rat{}, err
			}
			num, haveNum = 0, false
			i++
		default:
			return clock.Rat{}, clock.New(clock.KindInvalidInput, "parse dash",
				"unexpected character %q in duration %q", string(c), orig)
		}
	}
	r, err := clock.R(totalSec, 1)
	if err != nil {
		return clock.Rat{}, err
	}
	return r.Add(frac)
}

func mul64safe(a, b int64) (int64, error) {
	r := a * b
	if (b != 0 && r/b != a) || (a != 0 && r/a != b) {
		return 0, clock.New(clock.KindOverflow, "parse dash",
			"duration overflow %d*%d", a, b)
	}
	return r, nil
}
