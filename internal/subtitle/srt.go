package subtitle

import (
	"strings"

	"mediasync/internal/clock"
	"mediasync/internal/issue"
)

// ParseSRT parses SubRip text. Cues are returned in source order; a malformed
// cue is reported and skipped, never repaired.
func ParseSRT(input, src string) (Track, issue.List) {
	track := Track{Input: input, Kind: "srt"}
	var issues issue.List
	lines := splitLines(src)
	i := 0
	for i < len(lines) {
		// Skip blank lines and optional numeric index lines.
		if isBlank(lines[i]) {
			i++
			continue
		}
		lineNo := i + 1 // 1-based source line of this line
		index := len(track.Cues) + 1
		idxLineNo := lineNo
		if !strings.Contains(lines[i], "-->") {
			n, ok := parseIntStrict(strings.TrimSpace(lines[i]))
			if !ok {
				issues.Add(lineIssue(issue.MalformedSubtitle, input, lineNo,
					"expected cue index or timing, got %q", strings.TrimSpace(lines[i])))
				i++
				continue
			}
			index = n
			i++
			idxLineNo = i // i now points at the timing line, 1-based
		}
		if i >= len(lines) {
			issues.Add(lineIssue(issue.MalformedSubtitle, input, idxLineNo, "cue %d has no timing line", index))
			break
		}
		timingLn := i + 1
		startS, endS, ok := splitTiming(lines[i], "-->")
		if !ok {
			issues.Add(lineIssue(issue.MalformedSubtitle, input, timingLn,
				"cue %d has malformed timing line %q", index, strings.TrimSpace(lines[i])))
			i++
			continue
		}
		i++
		var text []string
		for i < len(lines) && !isBlank(lines[i]) {
			text = append(text, lines[i])
			i++
		}
		start, err := clock.ParseSRTTime(startS)
		if err != nil {
			issues.Add(lineIssue(issue.InvalidTime, input, timingLn, "cue %d start: %v", index, err))
			continue
		}
		end, err := clock.ParseSRTTime(endS)
		if err != nil {
			issues.Add(lineIssue(issue.InvalidTime, input, timingLn, "cue %d end: %v", index, err))
			continue
		}
		if err := clock.CueRange(start, end); err != nil {
			issues.Add(lineIssue(issue.NegativeDuration, input, timingLn, "cue %d: %v", index, err))
			continue
		}
		track.Cues = append(track.Cues, Cue{
			Index:   index,
			Start:   start,
			End:     end,
			Text:    strings.Join(text, "\n"),
			StartLn: idxLineNo,
			EndLn:   timingLn,
		})
	}
	issues.Add(overlapIssues(input, track.Cues)...)
	return track, issues
}
