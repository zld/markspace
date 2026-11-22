package subtitle

import (
	"strconv"
	"strings"

	"mediaarchive/internal/clock"
	"mediaarchive/internal/diag"
)

// parseVTT parses WebVTT from a BOM-stripped body. The header metadata,
// NOTE blocks, STYLE blocks and REGION blocks are skipped; cue identifiers,
// cue settings and multi-line payloads are preserved.
func parseVTT(data []byte, source string) *ParseResult {
	res := &ParseResult{Source: source}
	blocks := splitBlocks(data)
	for bi, b := range blocks {
		ls := lines(b)
		if len(ls) == 0 {
			continue
		}
		first := strings.TrimSpace(ls[0])
		if bi == 0 {
			if !strings.HasPrefix(first, "WEBVTT") {
				res.Faults = append(res.Faults, diag.New(diag.CodeTruncated, b.off,
					"webvtt file does not start with WEBVTT signature"))
			}
			continue
		}
		if strings.HasPrefix(first, "NOTE") || first == "STYLE" || first == "REGION" {
			continue
		}
		if len(b.raw) > MaxCueBytes {
			res.Faults = append(res.Faults, diag.New(diag.CodeTruncated, b.off,
				"webvtt cue block of %d bytes exceeds %d", len(b.raw), MaxCueBytes))
			continue
		}
		id := ""
		timingLine := 0
		if !strings.Contains(ls[0], "-->") {
			id = first
			timingLine = 1
		}
		if timingLine >= len(ls) {
			res.Faults = append(res.Faults, diag.New(diag.CodeTruncated, b.off,
				"webvtt cue %q has no timing line", id))
			continue
		}
		start, end, settings, err := parseVTTTiming(ls[timingLine])
		if err != nil {
			label := id
			if label == "" {
				label = "#" + strconv.Itoa(len(res.Cues)+1)
			}
			res.Faults = append(res.Faults, diag.New(diag.CodeInvalidTime, b.off,
				"webvtt cue %s timing: %v", label, err))
			continue
		}
		if end.Cmp(start) <= 0 {
			res.Faults = append(res.Faults, diag.New(diag.CodeInvalidTime, b.off,
				"webvtt cue %q ends before it starts", id))
			continue
		}
		res.Cues = append(res.Cues, Cue{
			Index:    len(res.Cues) + 1,
			ID:       id,
			Start:    start,
			End:      end,
			Text:     strings.Join(ls[timingLine+1:], "\n"),
			Settings: settings,
			Offset:   b.off,
			Source:   source,
		})
	}
	return res
}

// parseVTTTiming accepts "HH:MM:SS.mmm --> HH:MM:SS.mmm" and the abbreviated
// "MM:SS.mmm" form. Cue settings after the end timestamp are preserved
// verbatim so alignment/position metadata survives into the manifest.
func parseVTTTiming(line string) (clock.Rational, clock.Rational, string, error) {
	parts := strings.SplitN(line, "-->", 2)
	if len(parts) != 2 {
		return clock.Zero, clock.Zero, "", diag.New(diag.CodeInvalidTime, 0, "missing -->")
	}
	start, err := parseTimestamp(strings.TrimSpace(parts[0]))
	if err != nil {
		return clock.Zero, clock.Zero, "", err
	}
	rest := strings.TrimSpace(parts[1])
	endTok := rest
	settings := ""
	if sp := strings.IndexAny(rest, " \t"); sp >= 0 {
		endTok = rest[:sp]
		settings = strings.TrimSpace(rest[sp+1:])
	}
	end, err := parseTimestamp(endTok)
	if err != nil {
		return clock.Zero, clock.Zero, "", err
	}
	return start, end, settings, nil
}
