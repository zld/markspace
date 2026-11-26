package clock

import (
	"fmt"
	"strings"

	"mediasync/internal/issue"
)

// TimecodeEntry is one parsed line of a timecode list.
type TimecodeEntry struct {
	Line      int
	Rate      Rate
	Drop      bool
	Text      string
	TC        Timecode
	Ticks     Tick
	FrameDiff int64 // difference between exact frame time and converted ticks
}

// ParseTimecodeFile parses lines of "<rate> <HH:MM:SS:FF>" or
// "<rate> <HH:MM:SS;FF>". Rates are 24, 25, 29.97, 50 (also 29.97df/ndf).
// Bad entries are reported with their 1-based source line; nothing is
// repaired and no nominal frame rate is substituted.
func ParseTimecodeFile(input, src string) ([]TimecodeEntry, issue.List) {
	var out []TimecodeEntry
	var issues issue.List
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lineNo := i + 1
		fields := strings.Fields(line)
		if len(fields) != 2 {
			issues.Add(issue.New(issue.MalformedSubtitle, input,
				fmt.Sprintf("expected \"<rate> <timecode>\", got %q", line)).WithLine(lineNo))
			continue
		}
		rate, ok := ParseRate(fields[0])
		if !ok {
			issues.Add(issue.New(issue.UnsupportedFrameRate, input,
				fmt.Sprintf("unsupported frame rate %q", fields[0])).WithLine(lineNo))
			continue
		}
		tc, err := ParseTimecode(fields[1], rate)
		if err != nil {
			cat := issue.InvalidTime
			switch {
			case strings.Contains(err.Error(), ErrDroppedFrameUsed.Error()):
				cat = issue.InvalidDFDrop
			case strings.Contains(err.Error(), ErrTimecodeDrop.Error()):
				cat = issue.InvalidDFDrop
			}
			issues.Add(issue.New(cat, input, err.Error()).WithLine(lineNo).
				WithRef(fmt.Sprintf("tc:%d", lineNo)))
			continue
		}
		ticks, err := tc.Ticks()
		if err != nil {
			issues.Add(issue.New(issue.InvalidTime, input, err.Error()).WithLine(lineNo))
			continue
		}
		// The converted value must equal the exact rational frame time to
		// within one tick; a larger gap would mean the rate is mishandled.
		exact := Tick(tc.TotalFrames() * TickRate * rate.Den / rate.Num)
		diff := ticks - exact
		if diff < -1 || diff > 1 {
			issues.Add(issue.New(issue.InvalidTime, input,
				fmt.Sprintf("timecode conversion error %d ticks", diff)).WithLine(lineNo))
		}
		out = append(out, TimecodeEntry{
			Line: lineNo, Rate: rate, Drop: tc.Drop, Text: tc.String(),
			TC: tc, Ticks: ticks, FrameDiff: int64(diff),
		})
	}
	return out, issues
}
