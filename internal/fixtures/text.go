package fixtures

import (
	"fmt"
	"math"
	"strings"

	"mediaarchive/internal/clock"
)

// NormalSRT returns a valid 12-cue SRT. Two cues carry SMPTE 29.97 drop-frame
// labels so timecode mapping is exercised by the default demo.
func NormalSRT() string {
	var b strings.Builder
	for i := 0; i < 12; i++ {
		start := 400 + i*780
		end := start + 620
		fmt.Fprintf(&b, "%d\n%s --> %s\n", i+1, srtTime(start), srtTime(end))
		switch i {
		case 3:
			fmt.Fprintf(&b, "Field note TC %s rolls from the head leader\n", labelFor(start, 30000, 1001, clock.Format2997DF))
		case 7:
			fmt.Fprintf(&b, "Field note TC %s matches the slate\n", labelFor(start, 30000, 1001, clock.Format2997DF))
		default:
			fmt.Fprintf(&b, "Tape A segment %02d\n", i+1)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// MultiRateSRT returns 11 cues for the 25 fps asset.
func MultiRateSRT() string {
	var b strings.Builder
	for i := 0; i < 11; i++ {
		start := 300 + i*850
		end := start + 700
		fmt.Fprintf(&b, "%d\n%s --> %s\n", i+1, srtTime(start), srtTime(end))
		if i == 5 {
			fmt.Fprintf(&b, "PAL reel TC %s verified\n", labelFor(start, 25, 1, clock.Format25))
		} else {
			fmt.Fprintf(&b, "PAL reel cue %02d\n", i+1)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// FiftySRT returns 10 cues for a 50 fps reel with integer-timecode labels.
func FiftySRT() string {
	var b strings.Builder
	for i := 0; i < 8; i++ {
		start := 400 + i*900
		end := start + 600
		fmt.Fprintf(&b, "%d\n%s --> %s\n", i+1, srtTime(start), srtTime(end))
		if i == 4 {
			fmt.Fprintf(&b, "High-frame-rate reel TC %s checked\n", labelFor(start, 50, 1, clock.Format50))
		} else {
			fmt.Fprintf(&b, "HFR cue %02d\n", i+1)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// TwentyFourSRT returns 10 film-rate cues with a 24 fps label.
func TwentyFourSRT() string {
	var b strings.Builder
	for i := 0; i < 10; i++ {
		start := 400 + i*900
		end := start + 600
		fmt.Fprintf(&b, "%d\n%s --> %s\n", i+1, srtTime(start), srtTime(end))
		if i == 3 {
			fmt.Fprintf(&b, "Film reel TC %s verified\n", labelFor(start, 24, 1, clock.Format24))
		} else {
			fmt.Fprintf(&b, "Film cue %02d\n", i+1)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// DriftSRT returns 10 cues; timings are shifted by a fixed capture offset so
// the drift fixture's subtitle-to-media mapping is not a no-op.
func DriftSRT() string {
	var b strings.Builder
	for i := 0; i < 10; i++ {
		start := 450 + i*900
		end := start + 610
		fmt.Fprintf(&b, "%d\n%s --> %s\ndrift cue %02d\n\n", i+1, srtTime(start), srtTime(end), i+1)
	}
	return b.String()
}

// TruncatedSRT includes one cue past the 10 s media extent, which becomes an
// offset-bearing mapping fault in the truncated fixture.
func TruncatedSRT() string {
	var b strings.Builder
	for i := 0; i < 8; i++ {
		start := 400 + i*900
		end := start + 600
		fmt.Fprintf(&b, "%d\n%s --> %s\ntruncated cue %02d\n\n", i+1, srtTime(start), srtTime(end), i+1)
	}
	b.WriteString("9\n00:00:10,500 --> 00:00:11,200\npast the recovered tail\n")
	return b.String()
}

// BadSRT has a BOM, one invalid timestamp, one zero-length cue and a
// truncated final record, interleaved with recoverable cues.
func BadSRT() string {
	return "\xEF\xBB\xBF" +
		"1\n00:00:00,500 --> 00:00:01,200\nrecoverable one\n\n" +
		"2\n00:00:01,300 --> 00:00:01,300\nzero length cue\n\n" +
		"3\n00:00:01,800 --> 00:00:02,400\nrecoverable two\n\n" +
		"4\n00:0X:02,500 --> 00:00:03,000\nbad timestamp\n\n" +
		"5\n00:00:03,100 --> 00:00:03,800\nrecoverable three\n\n" +
		"6\n00:00:03,900 --> 00:00:04,500\nrecoverable four\n\n" +
		"7\n00:00:04,600 --> 00:00:05,300\nrecoverable five\n\n" +
		"8\n00:00:05,400 --> 00:00:06,000\nrecoverable six\n\n" +
		"9\n00:00:06,100 --> 00:00:06,700\nrecoverable seven\n\n" +
		"10\n00:00:06,800 --> 00:00:07,400\nrecoverable eight\n\n" +
		"11\n00:00:07,500 --> 00:00:08,100\nrecoverable nine\n\n" +
		"12\n00:00:08,200 --> 00:00:08,900\nrecoverable ten\n\n" +
		"13\n00:00:09,000 --> 00:00:09,600\ntruncated record"
}

// NormalVTT is used by the VFR fixture to prove WebVTT maps onto the same
// base as SRT, including identifiers and settings.
func NormalVTT() string {
	var b strings.Builder
	b.WriteString("WEBVTT - VFR reel\nKind: captions\n\nNOTE generated fixture\n\n")
	// Cues stay inside the 8.05 s VFR media extent: 8 cues end at 7.4 s.
	for i := 0; i < 8; i++ {
		start := 500 + i*900
		end := start + 600
		if i%3 == 0 {
			fmt.Fprintf(&b, "cue-%02d\n", i+1)
		}
		fmt.Fprintf(&b, "%s --> %s align:start position:10%%\nVFR cue %02d\n\n",
			vttTime(start), vttTime(end), i+1)
	}
	return b.String()
}

// labelFor converts a media millisecond offset into the SMPTE label that
// matches it on the given frame rate, so fixture cue text and cue timing are
// consistent and only genuine defects produce faults.
func labelFor(ms int, num, den int64, f clock.Format) string {
	frames := int64(math.Round(float64(ms) * float64(num) / (1000 * float64(den))))
	if frames < 0 {
		frames = 0
	}
	tc, err := clock.LabelFromFrame(frames, f)
	if err != nil {
		return "00:00:00:00"
	}
	return tc.String()
}

func srtTime(ms int) string {
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, (ms/60000)%60, (ms/1000)%60, ms%1000)
}

func vttTime(ms int) string {
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, (ms/60000)%60, (ms/1000)%60, ms%1000)
}
