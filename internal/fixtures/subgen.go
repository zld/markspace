package fixtures

import (
	"fmt"
	"strings"
)

// SubtitleFiles returns the generated caption inputs.
type SubtitleFiles struct {
	SRT string
	VTT string
}

// ReviewSRT is a healthy short SRT track covering the review window.
func ReviewSRT() string {
	return `1
00:00:00,500 --> 00:00:01,500
Picture freezes

2
00:00:02,000 --> 00:00:03,200
Audio continues
`
}

// ReviewVTT is a healthy WebVTT track aligned with the media samples.
func ReviewVTT() string {
	return `WEBVTT - review sample

00:00:00.480 --> 00:00:01.520
Picture freezes

00:00:02.000 --> 00:00:03.000
Audio continues
`
}

// BrokenSRT mixes valid cues with every damage class the parser must survive:
// a malformed timestamp, an inverted interval and an empty cue.
func BrokenSRT() string {
	return `1
00:00:00,500 --> 00:00:01,500
Survivor before

2
00:00:02,0x0 --> 00:00:03,000
Malformed timestamp

3
00:00:05,000 --> 00:00:04,000
Ends before it starts

4
00:00:06,000 --> 00:00:07,000


5
00:00:08,000 --> 00:00:09,000
Survivor after
`
}

// BrokenVTT has a zero-duration cue and a cue with no text.
func BrokenVTT() string {
	return `WEBVTT

00:00:01.000 --> 00:00:01.000
Zero duration

00:00:02.000 --> 00:00:03.000

00:00:04.000 --> 00:00:05.000
Survivor
`
}

// TimecodeSheet is a 29.97 drop-frame timecode validation input as ordered
// lines. Line 1 is a comment. Lines 2 and 3 are labels skipped by drop-frame
// counting, and line 4 has a frame number at the nominal rate (30), all of
// which must be rejected. The tenth-minute label, a drop-frame label inside a
// minute, and a non-drop label are valid and must not be flagged.
func TimecodeSheet() []string {
	return []string{
		"# timecode validation sheet (29.97 drop-frame)",
		"00:01:00;00",
		"00:01:00;01",
		"00:00:00;30",
		"00:10:00;00",
		"00:00:20;12",
		"00:00:00:25",
	}
}

// EventLines is the client event log used by the self-test. Each line is
// "id,client_ms,media_ms,label"; the final entry omits media time to exercise
// the event-only path.
func EventLines() []string {
	var out []string
	for i := 0; i < 4; i++ {
		media := i * 1000
		client := media
		if i == 2 {
			client = media + 90 // clock drift beyond the 40 ms budget
		}
		out = append(out, fmt.Sprintf("evt-%d,%d,%d,frame-%d", i, client, media, i))
	}
	out = append(out, "evt-4,5000,,trailing")
	return out
}

// EventsText renders EventLines as the on-disk event log.
func EventsText() string {
	return strings.Join(EventLines(), "\n") + "\n"
}
