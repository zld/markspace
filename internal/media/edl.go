package media

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// EDL line grammar, one directive per line ('#' starts a comment):
//
//	clip <track-key> <start-ns> <end-ns>
//	transition <from-track> <to-track> <start-ns> <end-ns>
//
// All intervals are half-open [start, end) on the unified nanosecond clock.
// Parse errors report the absolute byte offset of the offending line.

// ParseEDLFile reads an edit-decision list and attaches it to the timeline.
func ParseEDLFile(path string, tl *Timeline) error {
	f, err := os.Open(path)
	if err != nil {
		return NewError(StageScan, "edl.open", err.Error())
	}
	defer f.Close()
	return ParseEDL(f, tl)
}

// ParseEDL consumes EDL bytes. The reader must be positioned at the start so
// reported offsets match the underlying file.
func ParseEDL(r io.Reader, tl *Timeline) error {
	br := bufio.NewReader(r)
	var offset int64
	lineNo := 0
	for {
		lineNo++
		raw, err := br.ReadBytes('\n')
		read := int64(len(raw))
		line := strings.TrimSpace(string(raw))
		if line != "" && !strings.HasPrefix(line, "#") {
			if perr := parseEDLLine(line, offset, lineNo, tl); perr != nil {
				return perr
			}
		}
		offset += read
		if err != nil {
			if err == io.EOF {
				break
			}
			return NewError(StageScan, "edl.read", err.Error()).WithOffset(offset)
		}
	}
	if err := tl.ValidateClips(); err != nil {
		return err
	}
	return tl.ValidateTransitions()
}

func parseEDLLine(line string, offset int64, lineNo int, tl *Timeline) error {
	fields := strings.Fields(line)
	switch fields[0] {
	case "clip":
		if len(fields) != 4 {
			return edlSyntax(offset, lineNo, "clip needs 3 fields: track start end")
		}
		start, end, err := parseInterval(fields[2], fields[3])
		if err != nil {
			return edlSyntax(offset, lineNo, err.Error())
		}
		tl.Clips = append(tl.Clips, Clip{
			TrackKey: fields[1],
			Interval: Interval{StartNs: start, EndNs: end},
		})
	case "transition":
		if len(fields) != 5 {
			return edlSyntax(offset, lineNo, "transition needs 4 fields: from to start end")
		}
		start, end, err := parseInterval(fields[3], fields[4])
		if err != nil {
			return edlSyntax(offset, lineNo, err.Error())
		}
		tl.Transitions = append(tl.Transitions, Transition{
			FromTrack: fields[1],
			ToTrack:   fields[2],
			Interval:  Interval{StartNs: start, EndNs: end},
		})
	default:
		return edlSyntax(offset, lineNo, fmt.Sprintf("unknown directive %q", fields[0]))
	}
	return nil
}

func parseInterval(startStr, endStr string) (int64, int64, error) {
	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid interval start %q", startStr)
	}
	end, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid interval end %q", endStr)
	}
	if start < 0 || end <= start {
		return 0, 0, fmt.Errorf("interval [%d,%d) is empty or inverted", start, end)
	}
	return start, end, nil
}

func edlSyntax(offset int64, lineNo int, reason string) *Error {
	return NewError(StageScan, "edl.parse",
		fmt.Sprintf("line %d: %s", lineNo, reason)).WithOffset(offset)
}

// EDLText renders a timeline's clips and transitions back to canonical EDL
// bytes, so fixtures and tests share one serializer.
func EDLText(tl *Timeline) []byte {
	var buf bytes.Buffer
	for _, c := range tl.Clips {
		fmt.Fprintf(&buf, "clip %s %d %d\n", c.TrackKey, c.Interval.StartNs, c.Interval.EndNs)
	}
	for _, x := range tl.Transitions {
		fmt.Fprintf(&buf, "transition %s %s %d %d\n",
			x.FromTrack, x.ToTrack, x.Interval.StartNs, x.Interval.EndNs)
	}
	return buf.Bytes()
}
