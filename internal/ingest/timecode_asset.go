package ingest

import (
	"errors"
	"fmt"
	"strings"

	"mediad/internal/core"
	"mediad/internal/diagnose"
)

// TimecodeLabel is one parsed label in a timecode asset.
type TimecodeLabel struct {
	Timecode Timecode  `json:"timecode"`
	Span     core.Span `json:"span"`
	Rate     string    `json:"rate"`
}

// TimecodeAsset is a sidecar list of SMPTE labels. A leading "#rate R"
// directive selects the label rate (29.97 is the default).
type TimecodeAsset struct {
	Asset  string          `json:"asset"`
	Rate   string          `json:"rate"`
	Labels []TimecodeLabel `json:"labels"`
	Issues diagnose.Issues `json:"-"`
}

// ParseTimecodes parses a timecode sidecar. Invalid labels, including
// impossible drop-frame numbers, are rejected individually with their byte
// spans; later labels are still scanned.
func ParseTimecodes(asset *Asset) (*TimecodeAsset, error) {
	ta := &TimecodeAsset{Asset: asset.Name, Rate: Rate2997.Name}
	body, shift, _, badBOM := stripBOM(asset.Data)
	if badBOM != nil {
		ta.Issues.Add(diagnose.Issue{
			Category: diagnose.CatUnsupported, Stage: diagnose.StageIngest,
			Format: "timecode", Asset: asset.Name, Span: badBOM.span,
			Detail: "UTF-16 timecode requires UTF-8",
		})
		return ta, nil
	}
	rate := Rate2997
	var prev *Timecode
	for _, ln := range splitLines(body) {
		text := strings.TrimSpace(ln.Text)
		start, end := ln.Start+shift, ln.End+shift
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "#rate") {
			r, err := ParseRate(strings.TrimSpace(strings.TrimPrefix(text, "#rate")))
			if err != nil {
				ta.Issues.Add(timecodeIssue(asset.Name, start, end, err.Error()))
				continue
			}
			rate = r
			ta.Rate = r.Name
			continue
		}
		if strings.HasPrefix(text, "#") {
			continue
		}
		tc, err := ParseTimecode(text, rate, core.Span{Start: start, End: end})
		if err != nil {
			var tce *TimecodeError
			detail := err.Error()
			if errors.As(err, &tce) {
				detail = tce.Detail
			}
			ta.Issues.Add(timecodeIssue(asset.Name, start, end, detail))
			continue
		}
		if prev != nil && tc.FramesTotal < prev.FramesTotal {
			ta.Issues.Add(timecodeIssue(asset.Name, start, end,
				fmt.Sprintf("label %s precedes previous %s", tc.Raw, prev.Raw)))
			continue
		}
		prev = &tc
		ta.Labels = append(ta.Labels, TimecodeLabel{Timecode: tc, Span: core.Span{Start: start, End: end}, Rate: rate.Name})
	}
	return ta, nil
}

func timecodeIssue(asset string, start, end int64, detail string) diagnose.Issue {
	return diagnose.Issue{
		Category: diagnose.CatTimecodeLabel, Stage: diagnose.StageIngest,
		Format: "timecode", Asset: asset, Span: core.Span{Start: start, End: end},
		Detail: detail, Recoverable: true,
	}
}
