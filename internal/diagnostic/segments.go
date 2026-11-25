// Package diagnostic maps scan/timeline faults onto user-facing damaged
// timeline segments while preserving every originating stage and offset.
package diagnostic

import (
	"sort"

	"github.com/zld/media-check/internal/clock"
	"github.com/zld/media-check/internal/fault"
	"github.com/zld/media-check/internal/stream"
	"github.com/zld/media-check/internal/timeline"
)

type Cause struct {
	Stage      fault.Stage `json:"stage"`
	Code       fault.Code  `json:"code"`
	Message    string      `json:"message"`
	ByteOffset int64       `json:"byte_offset,omitempty"`
}

type Segment struct {
	ID             string      `json:"id"`
	Kind           string      `json:"kind"`
	Severity       string      `json:"severity"`
	TrackID        uint32      `json:"track_id,omitempty"`
	InputID        string      `json:"input_id,omitempty"`
	InputSHA256    string      `json:"input_sha256,omitempty"`
	FirstBadOffset int64       `json:"first_bad_offset,omitempty"`
	SampleOffset   int64       `json:"sample_offset,omitempty"`
	BoxOffset      int64       `json:"box_offset,omitempty"`
	TimeRange      clock.Range `json:"time_range,omitempty"`
	TimeRangeMS    clock.Range `json:"time_range_ms,omitempty"`
	StartMS        int64       `json:"start_ms"`
	EndMS          int64       `json:"end_ms"`
	Causes         []Cause     `json:"causes"`
}

type Report struct {
	DamageCount    int       `json:"damage_count"`
	FirstBadOffset int64     `json:"first_bad_offset,omitempty"`
	TrackID        uint32    `json:"track_id,omitempty"`
	StartMS        int64     `json:"start_ms,omitempty"`
	EndMS          int64     `json:"end_ms,omitempty"`
	Segments       []Segment `json:"segments"`
}

// Locate converts structured scan and timeline faults into merged damaged
// timeline ranges.
func Locate(scanRes *stream.Result, timelineRep *timeline.Report) *Report {
	rep := &Report{Segments: []Segment{}}
	if scanRes == nil {
		scanRes = &stream.Result{}
	}

	var candidates []Segment
	for _, issue := range scanRes.Issues {
		if issue == nil {
			continue
		}
		d := segmentFromIssue(issue)
		enrichFromSamples(&d, scanRes.Samples)
		finishTimeRange(&d)
		candidates = append(candidates, d)
	}
	if timelineRep != nil {
		for _, issue := range timelineRep.Issues {
			if issue == nil {
				continue
			}
			d := segmentFromIssue(issue)
			finishTimeRange(&d)
			candidates = append(candidates, d)
		}
	}

	merged := mergeCandidates(candidates)
	if merged == nil {
		merged = []Segment{}
	}
	for i := range merged {
		merged[i].ID = damageID(i, merged[i])
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].FirstBadOffset != merged[j].FirstBadOffset {
			return merged[i].FirstBadOffset < merged[j].FirstBadOffset
		}
		if merged[i].TrackID != merged[j].TrackID {
			return merged[i].TrackID < merged[j].TrackID
		}
		return merged[i].StartMS < merged[j].StartMS
	})
	for i := range merged {
		merged[i].ID = damageID(i, merged[i])
	}

	rep.Segments = merged
	rep.DamageCount = len(merged)
	if len(merged) > 0 {
		first := merged[0]
		rep.FirstBadOffset = first.FirstBadOffset
		rep.TrackID = first.TrackID
		rep.StartMS = first.StartMS
		rep.EndMS = first.EndMS
	}
	return rep
}

func segmentFromIssue(issue *fault.Error) Segment {
	return Segment{
		Kind:           kindFor(issue.Code),
		Severity:       "error",
		TrackID:        issue.TrackID,
		InputID:        issue.InputID,
		InputSHA256:    issue.InputSHA,
		FirstBadOffset: issue.Offset,
		SampleOffset:   issue.SampleOffset,
		BoxOffset:      issue.BoxOffset,
		TimeRange:      issue.TimeRange,
		Causes: []Cause{{
			Stage: issue.Stage, Code: issue.Code, Message: issue.Message, ByteOffset: issue.Offset,
		}},
	}
}

func finishTimeRange(d *Segment) {
	if ms, err := d.TimeRange.Milliseconds(); err == nil {
		d.TimeRangeMS = ms
		d.StartMS, d.EndMS = ms.Start, ms.End
	}
}

func enrichFromSamples(d *Segment, samples []stream.SampleReport) {
	if d.TrackID != 0 && d.TimeRange.Timescale != 0 {
		return
	}
	for _, s := range samples {
		if s.AbsoluteOffset < 0 || s.Size <= 0 {
			continue
		}
		end := s.AbsoluteOffset + int64(s.Size)
		if d.FirstBadOffset < s.AbsoluteOffset || d.FirstBadOffset > end {
			continue
		}
		if d.TrackID == 0 {
			d.TrackID = s.TrackID
		}
		if d.TimeRange.Timescale == 0 {
			d.TimeRange = s.TimeRange
		}
		if d.SampleOffset == 0 {
			d.SampleOffset = s.AbsoluteOffset
		}
		return
	}
}

func kindFor(code fault.Code) string {
	switch code {
	case fault.CodeTruncatedBox, fault.CodeUnreadableSample:
		return "truncated-box"
	case fault.CodeMissingKeyframe, fault.CodeKeyframeInterval:
		return "missing-keyframe"
	case fault.CodeTimestampRegression:
		return "timestamp-regression"
	default:
		return string(code)
	}
}

func mergeCandidates(in []Segment) []Segment {
	var out []Segment
	for _, d := range in {
		merged := false
		for i := range out {
			if canMerge(out[i], d) {
				out[i] = mergeOne(out[i], d)
				merged = true
				break
			}
		}
		if !merged {
			out = append(out, d)
		}
	}
	return out
}

func canMerge(a, b Segment) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.TrackID != 0 && b.TrackID != 0 && a.TrackID != b.TrackID {
		return false
	}
	// Same concrete sample/box evidence is always the same damage event.
	if a.SampleOffset != 0 && a.SampleOffset == b.SampleOffset {
		return true
	}
	if a.BoxOffset != 0 && a.BoxOffset == b.BoxOffset {
		return true
	}
	if a.TimeRangeMS.Timescale == 0 || b.TimeRangeMS.Timescale == 0 {
		return a.FirstBadOffset == b.FirstBadOffset
	}
	// Adjacent bad samples form one contiguous damaged section.
	return a.StartMS <= b.EndMS && b.StartMS <= a.EndMS
}

func rangesOverlap(a0, a1, b0, b1 int64) bool {
	return a0 < b1 && b0 < a1
}

func mergeOne(a, b Segment) Segment {
	if a.TrackID == 0 {
		a.TrackID = b.TrackID
	}
	if b.FirstBadOffset != 0 && (a.FirstBadOffset == 0 || b.FirstBadOffset < a.FirstBadOffset) {
		a.FirstBadOffset = b.FirstBadOffset
	}
	if a.SampleOffset == 0 {
		a.SampleOffset = b.SampleOffset
	}
	if a.BoxOffset == 0 {
		a.BoxOffset = b.BoxOffset
	}
	if a.InputSHA256 == "" {
		a.InputSHA256 = b.InputSHA256
	}
	if b.StartMS < a.StartMS || a.TimeRangeMS.Timescale == 0 {
		a.StartMS = b.StartMS
	}
	if b.EndMS > a.EndMS || a.TimeRangeMS.Timescale == 0 {
		a.EndMS = b.EndMS
	}
	if b.TimeRangeMS.Timescale != 0 {
		if a.TimeRangeMS.Timescale == 0 || b.TimeRangeMS.Start < a.TimeRangeMS.Start {
			a.TimeRangeMS.Start = b.TimeRangeMS.Start
		}
		if b.TimeRangeMS.End > a.TimeRangeMS.End {
			a.TimeRangeMS.End = b.TimeRangeMS.End
		}
		a.TimeRangeMS.Timescale = 1000
	}
	if b.TimeRange.Timescale != 0 && (a.TimeRange.Timescale == 0 || b.TimeRange.Start < a.TimeRange.Start) {
		a.TimeRange = b.TimeRange
	}
	a.Causes = append(a.Causes, b.Causes...)
	return a
}

func damageID(i int, s Segment) string {
	return s.Kind + "-t" + itoa(int(s.TrackID)) + "-" + itoa(i+1)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	pos := len(b)
	for v > 0 {
		pos--
		b[pos] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
