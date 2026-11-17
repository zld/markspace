// Package codec serializes frame-aligned cues back into SRT, WebVTT and ASS
// text. Frame boundary times are rounded to the precision each format can
// represent: milliseconds for SRT/VTT, centiseconds for ASS.
package codec

import (
	"fmt"
	"strings"
	"time"

	"subtitle/internal/cue"
	"subtitle/internal/timebase"
)

// OutputCue is one frame-aligned, conflict-resolved cue ready to write.
type OutputCue struct {
	Frames timebase.Frames
	Text   string
}

// Serialize renders cues in the requested format. Rate converts frame numbers
// back into media time.
func Serialize(cues []OutputCue, format cue.Format, rate timebase.Rate) (string, error) {
	switch format {
	case cue.FormatSRT:
		return serializeSRT(cues, rate), nil
	case cue.FormatVTT:
		return serializeVTT(cues, rate), nil
	case cue.FormatASS:
		return serializeASS(cues, rate), nil
	default:
		return "", fmt.Errorf("cannot serialize unknown format %q", format)
	}
}

func frameTimes(cues []OutputCue, rate timebase.Rate) []struct{ start, end time.Duration } {
	out := make([]struct{ start, end time.Duration }, len(cues))
	for i, c := range cues {
		out[i].start = rate.FrameTime(c.Frames.Start)
		out[i].end = rate.FrameTime(c.Frames.End)
	}
	return out
}

func srtClock(d time.Duration) string {
	ms := timebase.RoundMillis(d)
	h := ms / 3600000
	ms %= 3600000
	m := ms / 60000
	ms %= 60000
	s := ms / 1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms%1000)
}

func vttClock(d time.Duration) string {
	return strings.Replace(srtClock(d), ",", ".", 1)
}

func assClock(d time.Duration) string {
	cs := timebase.RoundCentis(d)
	h := cs / 360000
	cs %= 360000
	m := cs / 6000
	cs %= 6000
	s := cs / 100
	return fmt.Sprintf("%d:%02d:%02d.%02d", h, m, s, cs%100)
}

func serializeSRT(cues []OutputCue, rate timebase.Rate) string {
	times := frameTimes(cues, rate)
	var b strings.Builder
	for i, c := range cues {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n",
			i+1, srtClock(times[i].start), srtClock(times[i].end), c.Text)
	}
	return b.String()
}

func serializeVTT(cues []OutputCue, rate timebase.Rate) string {
	times := frameTimes(cues, rate)
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for i, c := range cues {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n",
			i+1, vttClock(times[i].start), vttClock(times[i].end), c.Text)
	}
	return b.String()
}

// assHeader is the fixed event layout written by this tool. The parser accepts
// any column order, so round-tripping third-party files still works.
const assHeader = "[Script Info]\n" +
	"ScriptType: v4.00+\n" +
	"WrapStyle: 0\n\n" +
	"[Events]\n" +
	"Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n"

func serializeASS(cues []OutputCue, rate timebase.Rate) string {
	times := frameTimes(cues, rate)
	var b strings.Builder
	b.WriteString(assHeader)
	for i, c := range cues {
		text := strings.ReplaceAll(c.Text, "\n", "\\N")
		fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Default,,0,0,0,,%s\n",
			assClock(times[i].start), assClock(times[i].end), text)
	}
	return b.String()
}
