package subtitle

import (
	"fmt"
	"strings"

	"mediqc/internal/clock"
)

// ticksToHMS renders ticks as HH:MM:SS.mmm components.
func ticksToHMS(t int64) (hh, mm, ss, ms int64) {
	total := t / (clock.Timescale / 1000)
	ms = total % 1000
	total /= 1000
	ss = total % 60
	total /= 60
	mm = total % 60
	hh = total / 60
	return hh, mm, ss, ms
}

// SerializeSRT renders a timeline back to SRT text.
func SerializeSRT(tl *Timeline) string {
	var b strings.Builder
	for i, c := range tl.Cues {
		sh, sm, ss, sms := ticksToHMS(c.Start)
		eh, em, es, ems := ticksToHMS(c.End)
		fmt.Fprintf(&b, "%d\n%02d:%02d:%02d,%03d --> %02d:%02d:%02d,%03d\n%s\n\n",
			i+1, sh, sm, ss, sms, eh, em, es, ems, c.Text)
	}
	return b.String()
}

// SerializeVTT renders a timeline back to WebVTT text.
func SerializeVTT(tl *Timeline) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for _, c := range tl.Cues {
		sh, sm, ss, sms := ticksToHMS(c.Start)
		eh, em, es, ems := ticksToHMS(c.End)
		fmt.Fprintf(&b, "%02d:%02d:%02d.%03d --> %02d:%02d:%02d.%03d\n%s\n\n",
			sh, sm, ss, sms, eh, em, es, ems, c.Text)
	}
	return b.String()
}

// Serialize renders a timeline in its own format.
func Serialize(tl *Timeline) string {
	if tl.Format == "vtt" {
		return SerializeVTT(tl)
	}
	return SerializeSRT(tl)
}
