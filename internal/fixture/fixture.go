// Package fixture generates all sample media assets in code: no binary media
// ships with the repository. It produces deterministic UTF-8 SRT/WebVTT
// subtitles, minimal MP4 sample indexes and RTP timestamp sidebands, plus the
// damaged variants used by the acceptance scenarios.
package fixture

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Case identifies a generated acceptance scenario.
type Case string

const (
	CaseNormal   Case = "normal"
	CaseDamaged  Case = "damaged"
	CaseConflict Case = "conflict"
	CaseRecovery Case = "recovery"
)

// Bundle is the set of generated files for one scenario.
type Bundle struct {
	Dir string

	// Subtitle source files (two versions per scenario).
	SRTPath string
	VTTPath string

	// MP4 sample index and its raw bytes.
	MP4Path string

	// RTP sideband capture.
	RTPPath string

	// Mapping table serialized for the run.
	MappingPath string

	// Damaged flags describing what was deliberately broken.
	Damaged []string
}

// ExpectedGap is a coverage hole the pipeline must record with provenance.
type ExpectedGap struct {
	Source  string
	Version string
	// StartSecond/EndSecond bound the uncovered timeline range.
	StartSecond int
	EndSecond   int
	Reason      string
}

// Layout returns the deterministic relative file layout under root.
func Layout(root string, c Case) *Bundle {
	base := filepath.Join(root, "sources", string(c))
	return &Bundle{
		Dir:         base,
		SRTPath:     filepath.Join(base, "subtitles.v1.srt"),
		VTTPath:     filepath.Join(base, "subtitles.v2.vtt"),
		MP4Path:     filepath.Join(base, "track.mp4"),
		RTPPath:     filepath.Join(base, "timestamps.rtp"),
		MappingPath: filepath.Join(base, "mapping.json"),
	}
}

// srtTime renders seconds.millis as an SRT HH:MM:SS,mmm timestamp.
func srtTime(sec, ms int) string {
	return fmt.Sprintf("%02d:%02d:%02d,%03d", sec/3600, (sec/60)%60, sec%60, ms)
}

// vttTime renders an HH:MM:SS.mmm timestamp.
func vttTime(sec, ms int) string {
	return fmt.Sprintf("%02d:%02d:%02d.%03d", sec/3600, (sec/60)%60, sec%60, ms)
}

// cueText is the deterministic payload shared across matching cues so that
// identical overlaps dedupe rather than conflict.
func cueText(n int) string {
	return fmt.Sprintf("cue line %d", n)
}

// normalCueCount is the number of well-formed cues each healthy source holds.
const normalCueCount = 8

// buildSRT renders the healthy 8-cue SRT source. Each cue spans one second
// starting at second n, matching the VTT source for dedupe coverage.
func buildSRT(damaged bool) string {
	var b strings.Builder
	for n := 1; n <= normalCueCount; n++ {
		start := n
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n",
			n, srtTime(start, 0), srtTime(start+1, 0), cueText(n))
	}
	if damaged {
		// A truncated trailing block: a bare cue index with no timestamp and
		// no text. A lenient parser skips it without counting a rejection,
		// while its expected time range is surfaced as a coverage gap.
		fmt.Fprintf(&b, "%d\n", normalCueCount+1)
	}
	return b.String()
}

// buildVTT renders the matching WebVTT source.
func buildVTT(damaged, conflict bool) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for n := 1; n <= normalCueCount; n++ {
		start := n
		text := cueText(n)
		if conflict && n == 4 {
			text = "conflicting alternate text"
		}
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n",
			n, vttTime(start, 0), vttTime(start+1, 0), text)
	}
	return b.String()
}
