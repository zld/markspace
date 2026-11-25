package subtitle

import (
	"strings"

	"mediasync/internal/clock"
	"mediasync/internal/issue"
)

// ParseVTT parses WebVTT. Clock times may be MM:SS.mmm; the optional cue
// identifier is preserved only as a position reference.
func ParseVTT(input, src string) (Track, issue.List) {
	track := Track{Input: input, Kind: "vtt"}
	var issues issue.List
	lines := splitLines(src)
	if len(lines) == 0 || !strings.HasPrefix(strings.TrimSpace(lines[0]), "WEBVTT") {
		issues.Add(lineIssue(issue.MalformedSubtitle, input, 1, "missing WEBVTT header"))
	}
	i := 1
	seenCue := 0
	for i < len(lines) {
		if isBlank(lines[i]) {
			i++
			continue
		}
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "NOTE") || strings.HasPrefix(trimmed, "STYLE") || strings.HasPrefix(trimmed, "REGION") {
			for i < len(lines) && !isBlank(lines[i]) {
				i++
			}
			continue
		}
		identLn := i + 1
		ident := ""
		if !strings.Contains(lines[i], "-->") {
			ident = trimmed
			i++
			if i >= len(lines) {
				issues.Add(lineIssue(issue.MalformedSubtitle, input, identLn, "cue %q has no timing line", ident))
				break
			}
		}
		timingLn := i + 1
		startS, endS, ok := splitTiming(lines[i], "-->")
		if !ok {
			issues.Add(lineIssue(issue.MalformedSubtitle, input, timingLn,
				"malformed cue timing %q", strings.TrimSpace(lines[i])))
			i++
			continue
		}
		i++
		var text []string
		for i < len(lines) && !isBlank(lines[i]) {
			text = append(text, lines[i])
			i++
		}
		seenCue++
		index := seenCue
		start, err := clock.ParseVTTTime(startS)
		if err != nil {
			issues.Add(lineIssue(issue.InvalidTime, input, timingLn, "cue %d start: %v", index, err))
			continue
		}
		end, err := clock.ParseVTTTime(endS)
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
			StartLn: identLn,
			EndLn:   timingLn,
		})
	}
	issues.Add(overlapIssues(input, track.Cues)...)
	return track, issues
}
